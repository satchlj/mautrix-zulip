// Package mathrender renders LaTeX math to PNG images using the Node.js helper in the math-renderer directory.
package mathrender

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type Rendered struct {
	PNG []byte
	// Intended display size in CSS pixels (the PNG has a higher resolution)
	Width  int
	Height int
}

type Options struct {
	NodePath     string
	RendererPath string
	Foreground   string
	Background   string
	Halo         string
}

type request struct {
	ID      int    `json:"id"`
	TeX     string `json:"tex"`
	Display bool   `json:"display"`
}

type response struct {
	ID     int    `json:"id"`
	PNG    string `json:"png"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Error  string `json:"error"`
}

// Renderer manages a long-running renderer process. It's started lazily and restarted if it dies.
type Renderer struct {
	opts Options
	log  zerolog.Logger

	lock    sync.Mutex
	stdin   io.WriteCloser
	pending map[int]chan *response
	nextID  int
}

func New(opts Options, log zerolog.Logger) *Renderer {
	return &Renderer{opts: opts, log: log}
}

var ErrRenderFailed = errors.New("failed to render math")

func (r *Renderer) start() error {
	cmd := exec.Command(r.opts.NodePath, filepath.Base(r.opts.RendererPath))
	cmd.Dir = filepath.Dir(r.opts.RendererPath)
	cmd.Env = append(os.Environ(),
		"MATH_FOREGROUND="+r.opts.Foreground,
		"MATH_BACKGROUND="+r.opts.Background,
		"MATH_HALO="+r.opts.Halo,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = r.log.With().Str("component", "math renderer stderr").Logger()
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("failed to start math renderer: %w", err)
	}
	r.stdin = stdin
	r.pending = make(map[int]chan *response)
	pending := r.pending
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
		for scanner.Scan() {
			var resp response
			if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
				r.log.Warn().Err(err).Msg("Failed to parse math renderer response")
				continue
			}
			r.lock.Lock()
			ch := pending[resp.ID]
			delete(pending, resp.ID)
			r.lock.Unlock()
			if ch != nil {
				ch <- &resp
			}
		}
		err := cmd.Wait()
		r.log.Warn().Err(err).Msg("Math renderer process exited")
		r.lock.Lock()
		if r.stdin == stdin {
			r.stdin = nil
		}
		for id, ch := range pending {
			close(ch)
			delete(pending, id)
		}
		r.lock.Unlock()
	}()
	return nil
}

// Render renders a LaTeX expression into a PNG. Display mode is used for block math.
func (r *Renderer) Render(ctx context.Context, tex string, display bool) (*Rendered, error) {
	r.lock.Lock()
	if r.stdin == nil {
		if err := r.start(); err != nil {
			r.lock.Unlock()
			return nil, err
		}
	}
	r.nextID++
	id := r.nextID
	ch := make(chan *response, 1)
	r.pending[id] = ch
	data, _ := json.Marshal(&request{ID: id, TeX: tex, Display: display})
	_, err := r.stdin.Write(append(data, '\n'))
	if err != nil {
		delete(r.pending, id)
		_ = r.stdin.Close()
		r.stdin = nil
		r.lock.Unlock()
		return nil, fmt.Errorf("failed to send request to math renderer: %w", err)
	}
	r.lock.Unlock()

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%w: renderer exited", ErrRenderFailed)
		} else if resp.Error != "" {
			return nil, fmt.Errorf("%w: %s", ErrRenderFailed, resp.Error)
		}
		png, err := base64.StdEncoding.DecodeString(resp.PNG)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64: %w", ErrRenderFailed, err)
		}
		return &Rendered{PNG: png, Width: resp.Width, Height: resp.Height}, nil
	case <-time.After(15 * time.Second):
		r.lock.Lock()
		delete(r.pending, id)
		r.lock.Unlock()
		return nil, fmt.Errorf("%w: timed out", ErrRenderFailed)
	case <-ctx.Done():
		r.lock.Lock()
		delete(r.pending, id)
		r.lock.Unlock()
		return nil, ctx.Err()
	}
}
