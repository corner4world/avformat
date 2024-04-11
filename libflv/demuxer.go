package libflv

import (
	"encoding/binary"
	"fmt"
	"github.com/yangjiechina/avformat/stream"
	"github.com/yangjiechina/avformat/utils"
)

type TagType byte
type VideoCodecId byte
type SoundFormat byte
type SoundRate byte

type TSMode byte

const (
	TagTypeAudioData        = TagType(8)
	TagTypeVideoData        = TagType(9)
	TagTypeScriptDataObject = TagType(18) //metadata value https://en.wikipedia.org/wiki/Flash_Video

	VideoCodeIdH263     = VideoCodecId(2)
	VideoCodeIdSCREEN   = VideoCodecId(3)
	VideoCodeIdVP6      = VideoCodecId(4)
	VideoCodeIdVP6Alpha = VideoCodecId(5)
	VideoCodeIdScreenV2 = VideoCodecId(6)
	VideoCodeIdH264     = VideoCodecId(7)

	SoundFormatMP3   = SoundFormat(2)
	SoundFormatG711A = SoundFormat(7)
	SoundFormatG711B = SoundFormat(8)
	SoundFormatAAC   = SoundFormat(10)
	SoundFormatMP38K = SoundFormat(14)

	SoundRate5500HZ  = SoundRate(0)
	SoundRate11000HZ = SoundRate(1)
	SoundRate22000HZ = SoundRate(2)
	SoundRate44000HZ = SoundRate(3) //For AAC:always 3

	TSModeAbsolute = TSMode(1)
	TSModeRelative = TSMode(2)

	AACFrameSize = 1024
	MP3FrameSize = 1152
)

type MP3Header uint32

type DeMuxer interface {
	stream.DeMuxer

	// InputVideo 输入不带tag的视频帧
	// @ts rtmp是相对时间
	//	   flv tag是绝对时间
	InputVideo(data []byte, ts uint32) error

	// InputAudio 输入不带tag的音频帧
	InputAudio(data []byte, ts uint32) error
}

type deMuxer struct {
	stream.DeMuxerImpl

	/**
	duration: DOUBLE
	width: DOUBLE
	height: DOUBLE
	videodatarate: DOUBLE
	framerate: DOUBLE
	videocodecid: DOUBLE
	audiosamplerate: DOUBLE
	audiosamplesize: DOUBLE
	stereo: BOOL
	audiocodecid: DOUBLE
	filesize: DOUBLE
	*/
	metaData []interface{}

	headerCompleted bool
	//保存当前正在读取的Tag
	tag Tag

	audioIndex int
	videoIndex int

	audioTs int64
	videoTs int64

	completed bool
	tsMode    TSMode

	videoStream utils.AVStream
	audioStream utils.AVStream
}

type Tag struct {
	preSize   uint32
	type_     TagType
	dataSize  int
	timestamp uint32

	data []byte
	size int
}

func NewDeMuxer(tsMode TSMode) DeMuxer {
	return &deMuxer{audioIndex: -1, videoIndex: -1, tsMode: tsMode}
}

func (d *deMuxer) readScriptDataObject(data []byte) error {
	buffer := utils.NewByteBuffer(data)

	if err := buffer.PeekCount(1); err != nil {
		return err
	}

	metaData, err := DoReadAFM0FromBuffer(buffer)
	if err != nil {
		return err
	}
	if len(metaData) <= 0 {
		return fmt.Errorf("invalid data")
	}
	if s, ok := metaData[0].(string); s == "" || !ok {
		return fmt.Errorf("not find the ONMETADATA of AMF0")
	}

	d.metaData = metaData
	return nil
}

func (d *deMuxer) readHeader(data []byte) error {
	if len(data) < 9 {
		return fmt.Errorf("the header of FLV requires 9 bytes")
	}

	if data[0] != 0x46 || data[1] != 0x4c || data[2] != 0x56 {
		return fmt.Errorf("the signature of FLV matching failed")
	}

	version := data[3]
	flags := typeFlag(data[4])
	dataOffset := binary.BigEndian.Uint32(data[5:])
	if version == 1 && dataOffset != 9 {
		return fmt.Errorf("invalid data")
	}

	if !flags.ExistAudio() && !flags.ExistVideo() {
		return fmt.Errorf("invalid data")
	}

	return nil
}

// 读取tag
// uint32 pre size
// TagType tag类型
// int data size
// uint32 timestamp
func (d *deMuxer) readTag(data []byte) Tag {
	_ = data[15]
	timestamp := utils.BytesToUInt24WhitSlice(data[8:])
	timestamp |= uint32(data[11]) << 24

	return Tag{preSize: binary.BigEndian.Uint32(data), type_: TagType(data[4]), dataSize: int(utils.BytesToUInt24WhitSlice(data[5:])), timestamp: timestamp}
}

func (d *deMuxer) parseTag(data []byte, tagType TagType, ts uint32) error {
	if TagTypeAudioData == tagType {
		err := d.InputAudio(data, ts)
		if err != nil {
			return err
		}
	} else if TagTypeVideoData == tagType {
		err := d.InputVideo(data, ts)
		if err != nil {
			return err
		}
	} else if TagTypeScriptDataObject == tagType {
		if err := d.readScriptDataObject(data); err != nil {
			return err
		}
	}

	return nil
}

// Input 输入tag
func (d *deMuxer) Input(data []byte) (int, error) {
	var n int
	if !d.headerCompleted {
		if err := d.readHeader(data); err != nil {
			return -1, err
		}

		d.headerCompleted = true
		n = 9
	}

	//读取未解析完的Tag
	need := d.tag.dataSize - d.tag.size
	if need > 0 {
		min := utils.MinInt(len(data), need)
		copy(d.tag.data[d.tag.size:], data[:min])
		d.tag.size += min
		n = min

		if min != need {
			return n, nil
		}

		err := d.parseTag(d.tag.data[:d.tag.size], d.tag.type_, d.tag.timestamp)
		if err != nil {
			return n, err
		}

		d.tag.size = 0
		d.tag.dataSize = 0
	}

	for len(data[n:]) > 15 {
		tag := d.readTag(data[n:])
		n += 15

		//数据不够，保存起，等下次
		if len(data[n:]) < tag.dataSize {
			tmp := d.tag.data
			d.tag = tag
			d.tag.data = tmp

			if cap(d.tag.data) < tag.dataSize {
				d.tag.data = make([]byte, tag.dataSize)
			}

			copy(d.tag.data, data[n:])
			d.tag.size = len(data[n:])
			n = len(data)
			break
		}

		err := d.parseTag(data[n:n+tag.dataSize], tag.type_, tag.timestamp)
		if err != nil {
			return n, err
		}

		n += tag.dataSize
	}

	return n, nil
}

// InputVideo 输入不带tag的视频帧
func (d *deMuxer) InputVideo(data []byte, ts uint32) error {
	n, sequenceHeader, key, codecId, ct, err := ParseVideoData(data)
	if err != nil {
		return err
	}

	if sequenceHeader {
		if d.completed {
			return nil
		}

		var stream utils.AVStream
		if d.audioIndex == -1 {
			d.videoIndex = 0
		} else {
			d.videoIndex = 1
		}

		stream = utils.NewAVStream(utils.AVMediaTypeVideo, d.videoIndex, codecId, data[n:], utils.ExtraTypeM4VC)
		extraData, err := stream.AnnexBExtraData()
		if err != nil {
			return err
		}

		err = fmt.Errorf("failed to parse SPS info")
		sps := utils.SPSInfo{}
		utils.SplitNalU(extraData, func(nalu []byte) {
			bytes := utils.RemoveStartCode(nalu)
			if utils.H264NalSPS == bytes[0]&0x1F {
				sps, err = utils.ParseSPS(bytes)
			}
		})

		if err != nil {
			return err
		}

		d.videoStream = utils.NewVideoStream(stream, sps.Width, sps.Height)
		d.Handler.OnDeMuxStream(d.videoStream)
		if d.audioIndex != -1 {
			d.Handler.OnDeMuxStreamDone()
		}
	} else {
		if d.videoIndex == -1 {
			return fmt.Errorf("missing video sequence header")
		}

		var duration int64
		if TSModeAbsolute == d.tsMode {
			duration = int64(ts) - d.videoTs
			d.videoTs = int64(ts)
		} else {
			d.videoTs += int64(ts)
			duration = int64(ts)
		}

		packet := utils.NewVideoPacket(data[n:], d.videoTs, d.videoTs+int64(ct), key, utils.PacketTypeAVCC, codecId, d.videoIndex, 1000)
		packet.SetDuration(duration)
		d.Handler.OnDeMuxPacket(packet)
	}

	return nil
}

func (d *deMuxer) InputAudio(data []byte, ts uint32) error {
	n, sequenceHeader, codecId, err := ParseAudioData(data)
	if err != nil {
		return err
	}

	if d.audioStream == nil {
		if d.completed {
			return nil
		}

		if d.videoIndex == -1 {
			d.audioIndex = 0
		} else {
			d.audioIndex = 1
		}

		var audioStream utils.AVStream
		if utils.AVCodecIdAAC == codecId && sequenceHeader {
			n = len(data)
			audioStream = utils.NewAVStream(utils.AVMediaTypeAudio, d.audioIndex, codecId, data[n:], utils.ExtraTypeNONE)
		} else {
			audioStream = utils.NewAVStream(utils.AVMediaTypeAudio, d.audioIndex, codecId, nil, utils.ExtraTypeNONE)
		}

		d.audioStream = audioStream
		d.Handler.OnDeMuxStream(d.audioStream)
		if d.videoIndex != -1 {
			d.Handler.OnDeMuxStreamDone()
		}

		if n >= len(data) {
			return nil
		}
	}

	if d.audioIndex == -1 {
		return fmt.Errorf("missing audio sequence header")
	}

	var duration int64
	if TSModeAbsolute == d.tsMode {
		duration = int64(ts) - d.audioTs
		d.audioTs = int64(ts)
	} else {
		d.audioTs += int64(ts)
		duration = int64(ts)
	}

	timeBase := 1000
	curTs := d.audioTs
	if d.audioStream.CodecId() == utils.AVCodecIdAAC {
		curTs = utils.ConvertTs(d.audioTs, 1000, AACFrameSize)
		duration += curTs - d.audioTs
		timeBase = AACFrameSize
	}

	packet := utils.NewAudioPacket(data[n:], curTs, curTs, codecId, d.audioIndex, timeBase)
	packet.SetDuration(duration)
	d.Handler.OnDeMuxPacket(packet)

	return nil
}

// ParseAudioData 解析音频数据
// @return int 音频帧起始偏移量，例如AAC AUDIO DATA跳过pkt type后的位置
// @return bool 是否是sequence header
func ParseAudioData(data []byte) (int, bool, utils.AVCodecID, error) {
	if len(data) < 4 {
		return -1, false, utils.AVCodecIdNONE, fmt.Errorf("invalid data")
	}

	soundFormat := data[0] >> 4
	//aac
	if byte(SoundFormatAAC) == soundFormat {
		//audio sequence header
		if data[1] == 0x0 {
			/*if len(data) < 4 {
				return -1, false, SoundFormat(0), fmt.Errorf("MPEG4 Audio Config requires at least 2 bytes")
			}*/

			return 2, true, utils.AVCodecIdAAC, nil
		} else if data[1] == 0x1 {
			return 2, false, utils.AVCodecIdAAC, nil
		}
	} else if byte(SoundFormatMP3) == soundFormat {
		return 1, false, utils.AVCodecIdMP3, nil
	} else if byte(SoundFormatG711A) == soundFormat {
		return 1, false, utils.AVCodecIdPCMALAW, nil
	} else if byte(SoundFormatG711B) == soundFormat {
		return 1, false, utils.AVCodecIdPCMMULAW, nil
	}

	return -1, false, utils.AVCodecIdNONE, fmt.Errorf("the codec %d is currently not supported in FLV", soundFormat)
}

func ParseVideoData(data []byte) (int, bool, bool, utils.AVCodecID, int, error) {
	if len(data) < 6 {
		return -1, false, false, utils.AVCodecIdNONE, 0, fmt.Errorf("invaild data")
	}

	frameType := data[0] >> 4
	codeId := data[0] & 0xF

	if byte(VideoCodeIdH264) == codeId {
		pktType := data[1]
		ct := utils.BytesToUInt24(data[2], data[3], data[4])

		return 5, pktType == 0, frameType == 1, utils.AVCodecIdH264, int(ct), nil
	} else if byte(VideoCodeIdH263) == codeId {
		//pktType := data[1]
		//ct := utils.BytesToUInt24(data[2], data[3], data[4])
		pktType := 1
		ct := 0
		return 0, pktType == 0, frameType == 1, utils.AVCodecIdH263, int(ct), nil
	}

	return -1, false, false, utils.AVCodecIdNONE, 0, fmt.Errorf("the codec %d is currently not supported in FLV", codeId)
}
