package propagation

type Sender interface {
	SendHeader(treeIdx int, to string, h BundleHeader) error
}

type ObservationSender interface {
	SendObservation(to string, obs FirstArrivalObservation) error
}

type ReceiverHandler interface {
	OnHeader(h BundleHeader, treeIdx int, from string) error
	OnProofs(bundleID [32]byte, proofs []ProofItem, from string) error
	OnObservation(obs FirstArrivalObservation, from string) error
}
