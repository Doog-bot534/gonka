package propagation

type ReceiverHandler interface {
	OnHeader(h BundleHeader, from string) error
}
