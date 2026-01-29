package propagation

type Sender interface {
	SendHeader(treeIdx int, to string, h BundleHeader) error
}

type ReceiverHandler interface {
	OnHeader(h BundleHeader, treeIdx int, from string) error
}
