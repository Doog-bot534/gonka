package propagation

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type diskWrite struct {
	header       BundleHeader
	arrivalTime  int64
	arrivalCount uint32
}

type verifiedWrite struct {
	header       BundleHeader
	arrivalTime  int64
	arrivalCount uint32
}

type forwardWork struct {
	header BundleHeader
	from   string
}

var (
	diskWritePool     = sync.Pool{New: func() any { return new(diskWrite) }}
	verifiedWritePool = sync.Pool{New: func() any { return new(verifiedWrite) }}
	forwardWorkPool   = sync.Pool{New: func() any { return new(forwardWork) }}
)

type headerState struct {
	arrivalTime int64
	verified    bool
}

type heightBucket struct {
	mu  sync.Mutex
	ids [][4]byte
}

type FLTQReceiver struct {
	cache    *Cache
	cube     *FLTQCube
	verifier PubKeyProvider
	myAddr   string
	sender   FLTQSender

	processedHeaders sync.Map // [4]byte -> *headerState
	blacklist        sync.Map // [4]byte -> struct{}
	heightBuckets    sync.Map // int64 -> *heightBucket

	mu         sync.RWMutex
	wg         sync.WaitGroup
	writeCh    chan *diskWrite // pointer: 8B/slot, saves ~150B/slot vs value type
	verifiedCh chan *verifiedWrite
	forwardCh  chan *forwardWork
	stopOnce   sync.Once
	stopCh     chan struct{}
}

func NewFLTQReceiver(cache *Cache, cube *FLTQCube, verifier PubKeyProvider, myAddr string, sender FLTQSender) *FLTQReceiver {
	r := &FLTQReceiver{
		cache:      cache,
		cube:       cube,
		verifier:   verifier,
		myAddr:     myAddr,
		sender:     sender,
		writeCh:    make(chan *diskWrite, 2000),
		verifiedCh: make(chan *verifiedWrite, 10000),
		forwardCh:  make(chan *forwardWork, 10000),
		stopCh:     make(chan struct{}),
	}

	numVerifiers := runtime.NumCPU()
	if numVerifiers > 4 {
		numVerifiers = 4
	}
	for i := 0; i < numVerifiers; i++ {
		r.wg.Add(1)
		go r.verifyWorker()
	}

	r.wg.Add(1)
	go r.diskWriter()

	numForwarders := runtime.NumCPU()
	if numForwarders > 2 {
		numForwarders = 2
	}
	for i := 0; i < numForwarders; i++ {
		r.wg.Add(1)
		go r.forwardWorker()
	}

	return r
}

func (r *FLTQReceiver) verifyWorker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.stopCh:
			return
		case dw := <-r.writeCh:
			pubKey, err := r.verifier.GetPubKey(dw.header.Participant)
			if err != nil {
				r.processedHeaders.Delete(dw.header.BundleID)
				r.blacklist.Store(dw.header.BundleID, struct{}{})
				diskWritePool.Put(dw)
				continue
			}

			if err := VerifyHeader(dw.header, pubKey); err != nil {
				r.processedHeaders.Delete(dw.header.BundleID)
				r.blacklist.Store(dw.header.BundleID, struct{}{})
				diskWritePool.Put(dw)
				continue
			}

			if stateVal, ok := r.processedHeaders.Load(dw.header.BundleID); ok {
				if state, ok := stateVal.(*headerState); ok {
					state.verified = true
				}
			}

			vw := verifiedWritePool.Get().(*verifiedWrite)
			*vw = verifiedWrite{
				header:       dw.header,
				arrivalTime:  dw.arrivalTime,
				arrivalCount: dw.arrivalCount,
			}
			diskWritePool.Put(dw)

			select {
			case r.verifiedCh <- vw:
			case <-r.stopCh:
				verifiedWritePool.Put(vw)
				return
			}
		}
	}
}

func (r *FLTQReceiver) diskWriter() {
	defer r.wg.Done()
	batch := make([]verifiedWrite, 0, 1000)
	for {
		select {
		case <-r.stopCh:
			return
		case vw := <-r.verifiedCh:
			batch = append(batch, *vw)
			verifiedWritePool.Put(vw)

			for len(batch) < 1000 {
				select {
				case vw2 := <-r.verifiedCh:
					batch = append(batch, *vw2)
					verifiedWritePool.Put(vw2)
				default:
					goto flush
				}
			}
		flush:
			if len(batch) > 0 {
				headers := make([]BundleHeader, len(batch))
				arrivals := make([]ArrivalInfo, len(batch))
				participants := make([]string, len(batch))
				pocHeights := make([]int64, len(batch))

				for i, w := range batch {
					headers[i] = w.header
					arrivals[i] = ArrivalInfo{Time: w.arrivalTime, Count: w.arrivalCount}
					participants[i] = w.header.Participant
					pocHeights[i] = w.header.PocHeight
				}

				_ = r.cache.StoreHeaderBatch(context.Background(), headers)
				_ = r.cache.StoreFirstArrivalBatch(context.Background(), arrivals, participants, pocHeights)
				_ = r.cache.FlushArrivals()
			}
			batch = batch[:0]
		}
	}
}

func (r *FLTQReceiver) forwardWorker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.stopCh:
			return
		case fw := <-r.forwardCh:
			h := fw.header
			from := fw.from
			forwardWorkPool.Put(fw)

			r.mu.RLock()
			cube := r.cube
			r.mu.RUnlock()
			r.forwardHeaderToNeighbors(h, cube, from)
		}
	}
}

func (r *FLTQReceiver) OnHeader(h BundleHeader, from string) error {
	if _, blacklisted := r.blacklist.Load(h.BundleID); blacklisted {
		return fmt.Errorf("header is blacklisted")
	}

	state := &headerState{
		arrivalTime: time.Now().UnixMilli(),
		verified:    false,
	}

	if _, loaded := r.processedHeaders.LoadOrStore(h.BundleID, state); loaded {
		return nil
	}

	expectedID := MakeBundleID(h.Participant, h.PocHeight, h.RootHash[:], h.Count)
	if !bytes.Equal(expectedID[:], h.BundleID[:]) {
		r.processedHeaders.Delete(h.BundleID)
		r.blacklist.Store(h.BundleID, struct{}{})
		return fmt.Errorf("bundle ID mismatch")
	}

	b, _ := r.heightBuckets.LoadOrStore(h.PocHeight, &heightBucket{})
	bucket := b.(*heightBucket)
	bucket.mu.Lock()
	bucket.ids = append(bucket.ids, h.BundleID)
	bucket.mu.Unlock()

	fw := forwardWorkPool.Get().(*forwardWork)
	*fw = forwardWork{header: h, from: from}
	select {
	case r.forwardCh <- fw:
	default:
		forwardWorkPool.Put(fw)
	}

	dw := diskWritePool.Get().(*diskWrite)
	*dw = diskWrite{
		header:       h,
		arrivalTime:  state.arrivalTime,
		arrivalCount: h.Count,
	}
	select {
	case r.writeCh <- dw:
	default:
		diskWritePool.Put(dw)
	}

	return nil
}

func (r *FLTQReceiver) forwardHeaderToNeighbors(h BundleHeader, cube *FLTQCube, from string) {
	if cube == nil {
		return
	}

	node := cube.GetNode(r.myAddr)
	if node == nil {
		return
	}

	for _, neighborAddr := range node.Neighbors {
		if neighborAddr != from {
			_ = r.sender.SendHeaderFLTQ(neighborAddr, h)
		}
	}
}

func (r *FLTQReceiver) SetFLTQCube(cube *FLTQCube) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cube = cube
}

func (r *FLTQReceiver) PurgeHeight(pocHeight int64) {
	val, ok := r.heightBuckets.LoadAndDelete(pocHeight)
	if !ok {
		return
	}
	bucket := val.(*heightBucket)
	bucket.mu.Lock()
	ids := bucket.ids
	bucket.ids = nil
	bucket.mu.Unlock()
	for _, id := range ids {
		r.processedHeaders.Delete(id)
		r.blacklist.Delete(id)
	}
}

func (r *FLTQReceiver) ClearProcessedState() {
	r.processedHeaders = sync.Map{}
	r.blacklist = sync.Map{}
	r.heightBuckets = sync.Map{}
}

func (r *FLTQReceiver) Wait() {
	for len(r.writeCh) > 0 || len(r.verifiedCh) > 0 || len(r.forwardCh) > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
}

func (r *FLTQReceiver) Close() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	r.wg.Wait()
}
