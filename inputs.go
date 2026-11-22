package rendobar

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Input is one value in CreateParams.Inputs. Use the type that matches what
// the job type takes (see Jobs.Schema for its input names):
//
//   - URL: one file the API fetches, such as {"source": rendobar.URL("https://...")}
//   - URLs: a list of files, such as image.edit's {"images": rendobar.URLs{...}}
//   - Content: a small text file sent inline, such as an ffmpeg concat list
//   - FromJob: a completed job's output, for ffmpeg chains
//   - Fonts: font files for text, under family names you choose
type Input interface {
	validate(c *check, path string)
	isInput()
}

// URL is a file the API fetches. An uploaded asset's content URL works too.
type URL string

// URLs is a list of files the API fetches, for an input that takes several.
type URLs []string

// Content is a small text file sent inline, up to 64 KiB, which ffmpeg reads
// under the input's name. It is sent as {"content": ...}.
type Content string

// FromJob uses the output of a completed job, by its id, as the input. It is
// sent as {"job": ...}. The API resolves it when the job runs.
type FromJob string

// Fonts are font files (TTF, OTF, TTC, WOFF or WOFF2) for a job that renders
// text. Reference each by its Family in the job's style params.
type Fonts []Font

// Font is one font file.
type Font struct {
	// Family is the name the params use for this font, such as "Display".
	// "" uses the family name inside the file.
	Family string `json:"family,omitempty"`
	Src    string `json:"src"` // the font file's URL
}

const maxContent = 64 << 10

var inputJobID = regexp.MustCompile(`^job_[A-Za-z0-9_-]+$`)

func (URL) isInput()     {}
func (URLs) isInput()    {}
func (Content) isInput() {}
func (FromJob) isInput() {}
func (Fonts) isInput()   {}

func (u URL) validate(c *check, path string) {
	if u == "" {
		c.fail(path, "is empty")
	}
}

func (u URLs) validate(c *check, path string) {
	if len(u) == 0 {
		c.fail(path, "has no URLs")
	}
	for i, v := range u {
		if v == "" {
			c.fail(fmt.Sprintf("%s.%d", path, i), "is empty")
		}
	}
}

func (s Content) validate(c *check, path string) {
	if len(s) > maxContent {
		c.fail(path, "is over 64 KiB")
	}
}

func (j FromJob) validate(c *check, path string) {
	if !inputJobID.MatchString(string(j)) {
		c.fail(path, "must be a job id, such as job_abc123")
	}
}

func (f Fonts) validate(c *check, path string) {
	if len(f) == 0 {
		c.fail(path, "has no fonts")
	}
	for i, font := range f {
		if font.Src == "" {
			c.fail(fmt.Sprintf("%s.%d.src", path, i), "is required")
		}
	}
}

// MarshalJSON sends {"content": ...}.
func (s Content) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Content string `json:"content"`
	}{string(s)})
}

// MarshalJSON sends {"job": ...}.
func (j FromJob) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Job string `json:"job"`
	}{string(j)})
}

// checkInputs refuses an input name the API would refuse: it becomes a file
// path when the job runs, so it must be a safe relative path.
func checkInputs(inputs map[string]Input, c *check) {
	for name, in := range inputs {
		path := "inputs." + name
		switch {
		case name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) ||
			(len(name) > 1 && name[1] == ':') || hasControl(name) || hasDotDot(name):
			c.fail(path, "must be a relative file name without .. segments")
		case in == nil:
			c.fail(path, "is nil")
		default:
			in.validate(c, path)
		}
	}
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return true
		}
	}
	return false
}

func hasDotDot(s string) bool {
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
