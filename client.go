package avformat

type Client interface {
	Open(url string)

	Close(url string)
}
