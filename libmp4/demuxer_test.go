package libmp4

import "testing"

func TestMp4DeMuxer(t *testing.T) {
	muxer := DeMuxer{}
	muxer.Read("../232937384-1-208_baseline.mp4")
	//muxer.Read("../LB1l2iXISzqK1RjSZFjXXblCFXa.mp4")
}
