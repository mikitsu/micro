package buffer

import (
	"bufio"
	"crypto/md5"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zyedidia/micro/cmd/micro/config"
	"github.com/zyedidia/micro/cmd/micro/highlight"

	. "github.com/zyedidia/micro/cmd/micro/util"
)

// LargeFileThreshold is the number of bytes when fastdirty is forced
// because hashing is too slow
const LargeFileThreshold = 50000

// overwriteFile opens the given file for writing, truncating if one exists, and then calls
// the supplied function with the file as io.Writer object, also making sure the file is
// closed afterwards.
func overwriteFile(name string, fn func(io.Writer) error) (err error) {
	var file *os.File

	if file, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err != nil {
		return
	}

	defer func() {
		if e := file.Close(); e != nil && err == nil {
			err = e
		}
	}()

	w := bufio.NewWriter(file)

	if err = fn(w); err != nil {
		return
	}

	err = w.Flush()
	return
}

// The BufType defines what kind of buffer this is
type BufType struct {
	Kind     int
	Readonly bool // The file cannot be edited
	Scratch  bool // The file cannot be saved
}

var (
	BTDefault = BufType{0, false, false}
	BTHelp    = BufType{1, true, true}
	BTLog     = BufType{2, true, true}
	BTScratch = BufType{3, false, true}
	BTRaw     = BufType{4, true, true}
)

type Buffer struct {
	*LineArray
	*EventHandler

	cursors     []*Cursor
	StartCursor Loc

	// Path to the file on disk
	Path string
	// Absolute path to the file on disk
	AbsPath string
	// Name of the buffer on the status line
	name string

	// Whether or not the buffer has been modified since it was opened
	isModified bool

	// Stores the last modification time of the file the buffer is pointing to
	ModTime time.Time

	SyntaxDef   *highlight.Def
	Highlighter *highlight.Highlighter

	// Hash of the original buffer -- empty if fastdirty is on
	origHash [md5.Size]byte

	// Settings customized by the user
	Settings map[string]interface{}

	// Type of the buffer (e.g. help, raw, scratch etc..)
	Type BufType
}

// NewBufferFromFile opens a new buffer using the given path
// It will also automatically handle `~`, and line/column with filename:l:c
// It will return an empty buffer if the path does not exist
// and an error if the file is a directory
func NewBufferFromFile(path string) (*Buffer, error) {
	var err error
	filename, cursorPosition := GetPathAndCursorPosition(path)
	filename, err = ReplaceHome(filename)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(filename)
	fileInfo, _ := os.Stat(filename)

	if err == nil && fileInfo.IsDir() {
		return nil, errors.New(filename + " is a directory")
	}

	defer file.Close()

	var buf *Buffer
	if err != nil {
		// File does not exist -- create an empty buffer with that name
		buf = NewBufferFromString("", filename)
	} else {
		buf = NewBuffer(file, FSize(file), filename, cursorPosition)
	}

	return buf, nil
}

// NewBufferFromString creates a new buffer containing the given string
func NewBufferFromString(text, path string) *Buffer {
	return NewBuffer(strings.NewReader(text), int64(len(text)), path, nil)
}

// NewBuffer creates a new buffer from a given reader with a given path
// Ensure that ReadSettings and InitGlobalSettings have been called before creating
// a new buffer
func NewBuffer(reader io.Reader, size int64, path string, cursorPosition []string) *Buffer {
	b := new(Buffer)

	b.Settings = config.DefaultLocalSettings()
	for k, v := range config.GlobalSettings {
		if _, ok := b.Settings[k]; ok {
			b.Settings[k] = v
		}
	}
	config.InitLocalSettings(b.Settings, b.Path)

	b.LineArray = NewLineArray(uint64(size), FFAuto, reader)

	absPath, _ := filepath.Abs(path)

	b.Path = path
	b.AbsPath = absPath

	// The last time this file was modified
	b.ModTime, _ = GetModTime(b.Path)

	b.EventHandler = NewEventHandler(b)

	b.UpdateRules()

	if _, err := os.Stat(config.ConfigDir + "/buffers/"); os.IsNotExist(err) {
		os.Mkdir(config.ConfigDir+"/buffers/", os.ModePerm)
	}

	// cursorLocation, err := GetBufferCursorLocation(cursorPosition, b)
	// b.startcursor = Cursor{
	// 	Loc: cursorLocation,
	// 	buf: b,
	// }
	// TODO flagstartpos
	if b.Settings["savecursor"].(bool) || b.Settings["saveundo"].(bool) {
		err := b.Unserialize()
		if err != nil {
			TermMessage(err)
		}
	}

	if !b.Settings["fastdirty"].(bool) {
		if size > LargeFileThreshold {
			// If the file is larger than LargeFileThreshold fastdirty needs to be on
			b.Settings["fastdirty"] = true
		} else {
			calcHash(b, &b.origHash)
		}
	}

	return b
}

// GetName returns the name that should be displayed in the statusline
// for this buffer
func (b *Buffer) GetName() string {
	if b.name == "" {
		if b.Path == "" {
			return "No name"
		}
		return b.Path
	}
	return b.name
}

// FileType returns the buffer's filetype
func (b *Buffer) FileType() string {
	return b.Settings["filetype"].(string)
}

// ReOpen reloads the current buffer from disk
func (b *Buffer) ReOpen() error {
	data, err := ioutil.ReadFile(b.Path)
	txt := string(data)

	if err != nil {
		return err
	}
	b.EventHandler.ApplyDiff(txt)

	b.ModTime, err = GetModTime(b.Path)
	b.isModified = false
	return err
	// TODO: buffer cursor
	// b.Cursor.Relocate()
}

func (b *Buffer) SetCursors(c []*Cursor) {
	b.cursors = c
}

func (b *Buffer) GetActiveCursor() *Cursor {
	return b.cursors[0]
}

func (b *Buffer) GetCursor(n int) *Cursor {
	return b.cursors[n]
}

func (b *Buffer) GetCursors() []*Cursor {
	return b.cursors
}

func (b *Buffer) NumCursors() int {
	return len(b.cursors)
}

func (b *Buffer) LineBytes(n int) []byte {
	if n >= len(b.lines) || n < 0 {
		return []byte{}
	}
	return b.lines[n].data
}

func (b *Buffer) LinesNum() int {
	return len(b.lines)
}

func (b *Buffer) Start() Loc {
	return Loc{0, 0}
}

// End returns the location of the last character in the buffer
func (b *Buffer) End() Loc {
	numlines := len(b.lines)
	return Loc{utf8.RuneCount(b.lines[numlines-1].data), numlines - 1}
}

// RuneAt returns the rune at a given location in the buffer
func (b *Buffer) RuneAt(loc Loc) rune {
	line := b.LineBytes(loc.Y)
	if len(line) > 0 {
		i := 0
		for len(line) > 0 {
			r, size := utf8.DecodeRune(line)
			line = line[size:]
			i++

			if i == loc.X {
				return r
			}
		}
	}
	return '\n'
}

// Modified returns if this buffer has been modified since
// being opened
func (b *Buffer) Modified() bool {
	if b.Settings["fastdirty"].(bool) {
		return b.isModified
	}

	var buff [md5.Size]byte

	calcHash(b, &buff)
	return buff != b.origHash
}

// calcHash calculates md5 hash of all lines in the buffer
func calcHash(b *Buffer, out *[md5.Size]byte) {
	h := md5.New()

	if len(b.lines) > 0 {
		h.Write(b.lines[0].data)

		for _, l := range b.lines[1:] {
			h.Write([]byte{'\n'})
			h.Write(l.data)
		}
	}

	h.Sum((*out)[:0])
}

func (b *Buffer) insert(pos Loc, value []byte) {
	b.isModified = true
	b.LineArray.insert(pos, value)
}
func (b *Buffer) remove(start, end Loc) []byte {
	b.isModified = true
	sub := b.LineArray.remove(start, end)
	return sub
}
func (b *Buffer) deleteToEnd(start Loc) {
	b.isModified = true
	b.LineArray.deleteToEnd(start)
}

// UpdateRules updates the syntax rules and filetype for this buffer
// This is called when the colorscheme changes
func (b *Buffer) UpdateRules() {
	rehighlight := false
	var files []*highlight.File
	for _, f := range config.ListRuntimeFiles(config.RTSyntax) {
		data, err := f.Data()
		if err != nil {
			TermMessage("Error loading syntax file " + f.Name() + ": " + err.Error())
		} else {
			file, err := highlight.ParseFile(data)
			if err != nil {
				TermMessage("Error loading syntax file " + f.Name() + ": " + err.Error())
				continue
			}
			ftdetect, err := highlight.ParseFtDetect(file)
			if err != nil {
				TermMessage("Error loading syntax file " + f.Name() + ": " + err.Error())
				continue
			}

			ft := b.Settings["filetype"].(string)
			if (ft == "Unknown" || ft == "") && !rehighlight {
				if highlight.MatchFiletype(ftdetect, b.Path, b.lines[0].data) {
					header := new(highlight.Header)
					header.FileType = file.FileType
					header.FtDetect = ftdetect
					b.SyntaxDef, err = highlight.ParseDef(file, header)
					if err != nil {
						TermMessage("Error loading syntax file " + f.Name() + ": " + err.Error())
						continue
					}
					rehighlight = true
				}
			} else {
				if file.FileType == ft && !rehighlight {
					header := new(highlight.Header)
					header.FileType = file.FileType
					header.FtDetect = ftdetect
					b.SyntaxDef, err = highlight.ParseDef(file, header)
					if err != nil {
						TermMessage("Error loading syntax file " + f.Name() + ": " + err.Error())
						continue
					}
					rehighlight = true
				}
			}
			files = append(files, file)
		}
	}

	if b.SyntaxDef != nil {
		highlight.ResolveIncludes(b.SyntaxDef, files)
	}

	if b.Highlighter == nil || rehighlight {
		if b.SyntaxDef != nil {
			b.Settings["filetype"] = b.SyntaxDef.FileType
			b.Highlighter = highlight.NewHighlighter(b.SyntaxDef)
			if b.Settings["syntax"].(bool) {
				b.Highlighter.HighlightStates(b)
			}
		}
	}
}

func (b *Buffer) IndentString(tabsize int) string {
	if b.Settings["tabstospaces"].(bool) {
		return Spaces(tabsize)
	}
	return "\t"
}