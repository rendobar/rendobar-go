package rendobar

import (
	"encoding/json"
	"fmt"
	"time"
)

// FfprobeResult is the output of an ffprobe job, from JobComplete.Ffprobe.
//
// The API's spec leaves the output data untyped, so this type follows the
// shape the API documents for ffprobe. It is open: the API may add fields,
// which this type ignores until a release reads them.
type FfprobeResult struct {
	// Summary is the media at a glance, or nil when ffprobe reported no
	// format or no streams.
	Summary *ProbeSummary
	// Format, Streams and Chapters are ffprobe's own sections as it wrote
	// them (snake_case JSON), or nil when absent.
	Format   json.RawMessage
	Streams  json.RawMessage
	Chapters json.RawMessage
	// Warnings are problems ffprobe reported that did not stop it.
	Warnings []ProbeWarning
	// Stdout is ffprobe's output when the command asked for a format other
	// than JSON, else "".
	Stdout string
}

// ProbeKind is what a probed file holds. New values may appear.
type ProbeKind string

// The ProbeKind values this SDK knows.
const (
	ProbeKindVideo ProbeKind = "video"
	ProbeKindAudio ProbeKind = "audio"
	ProbeKindImage ProbeKind = "image"
	ProbeKindOther ProbeKind = "other"
)

// ProbeSummary is the media of a probed file at a glance. A number or time
// ffprobe did not report is zero.
type ProbeSummary struct {
	Kind           ProbeKind
	Container      string // such as mp4
	FormatLongName string // such as QuickTime / MOV
	Duration       time.Duration
	StartTime      time.Duration
	SizeBytes      int64
	BitrateBps     int64
	StreamCounts   StreamCounts
	Tags           map[string]string // container tags, such as encoder
	// Video is the main video stream, set when Kind is video.
	Video *ProbeVideo
	// Audio is the main audio stream, set when Kind is audio, and for a
	// video with sound.
	Audio *ProbeAudio
	// Image is set when Kind is image.
	Image *ProbeImage
}

// StreamCounts counts a file's streams by kind.
type StreamCounts struct {
	Video, Audio, Subtitle, Data, Attachment int
}

// ProbeVideo is the main video stream of a probed file.
type ProbeVideo struct {
	Codec              string // such as h264
	Profile            string
	Width, Height      int    // pixels
	DisplayAspectRatio string // such as 16:9
	PixelFormat        string // such as yuv420p
	BitDepth           int
	FPS                float64
	VariableFrameRate  bool
	Rotation           int // degrees: 0, 90, 180 or 270
	HDR                bool
	BitrateBps         int64
	Language           string
}

// ProbeAudio is the main audio stream of a probed file.
type ProbeAudio struct {
	Codec         string // such as aac
	Profile       string
	Channels      int
	ChannelLayout string // such as stereo
	SampleRate    int    // Hz
	BitrateBps    int64
	Language      string
}

// ProbeImage describes a probed image.
type ProbeImage struct {
	Codec         string
	Width, Height int
	PixelFormat   string
	BitDepth      int
}

// ProbeWarning is a problem ffprobe reported.
type ProbeWarning struct {
	Code    string
	Message string
}

// Ffprobe reads the output of a complete ffprobe job. It fails when the job
// is another type or its data is not an ffprobe result.
func (c *JobComplete) Ffprobe() (*FfprobeResult, error) {
	if c.Type != (FfprobeParams{}).JobType() {
		return nil, fmt.Errorf("rendobar: job %s is a %s job, not ffprobe", c.ID, c.Type)
	}
	if c.Output.Data == nil {
		return nil, fmt.Errorf("rendobar: ffprobe job %s has no output data", c.ID)
	}
	var w struct {
		Summary  *wireProbeSummary `json:"summary"`
		Format   json.RawMessage   `json:"format"`
		Streams  json.RawMessage   `json:"streams"`
		Chapters json.RawMessage   `json:"chapters"`
		Warnings []ProbeWarning    `json:"warnings"`
		Stdout   *string           `json:"stdout"`
	}
	if err := json.Unmarshal(c.Output.Data, &w); err != nil {
		return nil, fmt.Errorf("rendobar: ffprobe job %s has unreadable output data: %w", c.ID, err)
	}
	out := &FfprobeResult{
		Format: present(w.Format), Streams: present(w.Streams), Chapters: present(w.Chapters),
		Warnings: w.Warnings, Stdout: str(w.Stdout),
	}
	if s := w.Summary; s != nil {
		out.Summary = s.summary()
	}
	return out, nil
}

// The wire shape of the summary. Every number may be null.
type wireProbeSummary struct {
	Kind           string            `json:"kind"`
	Container      *string           `json:"container"`
	FormatLongName *string           `json:"formatLongName"`
	DurationSec    *float64          `json:"durationSec"`
	StartTimeSec   *float64          `json:"startTimeSec"`
	SizeBytes      *float64          `json:"sizeBytes"`
	BitrateBps     *float64          `json:"bitrateBps"`
	StreamCounts   map[string]int    `json:"streamCounts"`
	Tags           map[string]string `json:"tags"`
	Video          *struct {
		Codec               *string  `json:"codec"`
		Profile             *string  `json:"profile"`
		Width               *float64 `json:"width"`
		Height              *float64 `json:"height"`
		DisplayAspectRatio  *string  `json:"displayAspectRatio"`
		PixelFormat         *string  `json:"pixelFormat"`
		BitDepth            *float64 `json:"bitDepth"`
		FPS                 *float64 `json:"fps"`
		IsVariableFrameRate bool     `json:"isVariableFrameRate"`
		Rotation            *float64 `json:"rotation"`
		IsHdr               bool     `json:"isHdr"`
		BitrateBps          *float64 `json:"bitrateBps"`
		Language            *string  `json:"language"`
	} `json:"video"`
	Audio *struct {
		Codec         *string  `json:"codec"`
		Profile       *string  `json:"profile"`
		Channels      *float64 `json:"channels"`
		ChannelLayout *string  `json:"channelLayout"`
		SampleRate    *float64 `json:"sampleRate"`
		BitrateBps    *float64 `json:"bitrateBps"`
		Language      *string  `json:"language"`
	} `json:"audio"`
	Image *struct {
		Codec       *string  `json:"codec"`
		Width       *float64 `json:"width"`
		Height      *float64 `json:"height"`
		PixelFormat *string  `json:"pixelFormat"`
		BitDepth    *float64 `json:"bitDepth"`
	} `json:"image"`
}

func (s *wireProbeSummary) summary() *ProbeSummary {
	out := &ProbeSummary{
		Kind: ProbeKind(s.Kind), Container: str(s.Container), FormatLongName: str(s.FormatLongName),
		Duration: secs(s.DurationSec), StartTime: secs(s.StartTimeSec),
		SizeBytes: int64(num(s.SizeBytes)), BitrateBps: int64(num(s.BitrateBps)), Tags: s.Tags,
		StreamCounts: StreamCounts{
			Video: s.StreamCounts["video"], Audio: s.StreamCounts["audio"], Subtitle: s.StreamCounts["subtitle"],
			Data: s.StreamCounts["data"], Attachment: s.StreamCounts["attachment"],
		},
	}
	if v := s.Video; v != nil {
		out.Video = &ProbeVideo{
			Codec: str(v.Codec), Profile: str(v.Profile), Width: int(num(v.Width)), Height: int(num(v.Height)),
			DisplayAspectRatio: str(v.DisplayAspectRatio), PixelFormat: str(v.PixelFormat), BitDepth: int(num(v.BitDepth)),
			FPS: num(v.FPS), VariableFrameRate: v.IsVariableFrameRate, Rotation: int(num(v.Rotation)), HDR: v.IsHdr,
			BitrateBps: int64(num(v.BitrateBps)), Language: str(v.Language),
		}
	}
	if a := s.Audio; a != nil {
		out.Audio = &ProbeAudio{
			Codec: str(a.Codec), Profile: str(a.Profile), Channels: int(num(a.Channels)), ChannelLayout: str(a.ChannelLayout),
			SampleRate: int(num(a.SampleRate)), BitrateBps: int64(num(a.BitrateBps)), Language: str(a.Language),
		}
	}
	if i := s.Image; i != nil {
		out.Image = &ProbeImage{
			Codec: str(i.Codec), Width: int(num(i.Width)), Height: int(num(i.Height)),
			PixelFormat: str(i.PixelFormat), BitDepth: int(num(i.BitDepth)),
		}
	}
	return out
}
