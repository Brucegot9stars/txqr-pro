package sender

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/divan/txqr"
)

const initialFrames = 64
const prefetchFrames = 32

var errMissingSession = errors.New("session not found")
var errFrameIndex = errors.New("bad frame index")

type frameRender struct {
	done chan struct{}
	data []byte
	err  error
}

// All cache metadata is protected by Server.mu. PNG buffers are immutable.
func (s *session) initCache() {
	if s.cacheReady {
		return
	}
	for _, f := range s.frames {
		s.rawBytes += int64(len(f))
	}
	for i, p := range s.pngs {
		if p != nil {
			s.pngBytes += int64(len(p))
			s.pngOrder = append(s.pngOrder, i)
		}
	}
	s.pending = make(map[int]*frameRender)
	s.cacheReady = true
}

func (s *session) evictPNG() bool {
	for s.pngHead < len(s.pngOrder) {
		i := s.pngOrder[s.pngHead]
		s.pngHead++
		if s.pngs[i] == nil {
			continue
		}
		s.pngBytes -= int64(len(s.pngs[i]))
		s.pngs[i] = nil
		if s.pngHead > 1024 {
			s.pngOrder = append([]int(nil), s.pngOrder[s.pngHead:]...)
			s.pngHead = 0
		}
		return true
	}
	return false
}

func (s *Server) retainedLocked() int64 {
	var n int64
	for _, sess := range s.sessions {
		sess.initCache()
		n += sess.rawBytes + sess.pngBytes
	}
	return n
}

func (s *Server) evictAnyLocked() bool {
	for _, sess := range s.sessions {
		if sess.evictPNG() {
			return true
		}
	}
	return false
}

func (s *Server) framePNG(ctx context.Context, id string, index int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	sess := s.sessions[id]
	if sess == nil {
		s.mu.Unlock()
		return nil, errMissingSession
	}
	if index < 0 || index >= len(sess.frames) || index >= len(sess.pngs) {
		s.mu.Unlock()
		return nil, errFrameIndex
	}
	sess.initCache()
	sess.lastUsed = time.Now()
	if png := sess.pngs[index]; png != nil {
		s.mu.Unlock()
		return png, nil
	}
	if call := sess.pending[index]; call != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.done:
			return call.data, call.err
		}
	}
	call := &frameRender{done: make(chan struct{})}
	sess.pending[index] = call
	s.mu.Unlock()
	start := time.Now()
	var png []byte
	var err error
	select {
	case s.renderSlots <- struct{}{}:
		// Unlike fountain solving, this work has fixed small input/geometry.
		// Never run arbitrary worker output through an unbounded image allocation.
		if sess.modules < 21 || sess.modules > 185 || sess.ppm < minPPM || sess.ppm > maxPPM || len(sess.frames[index]) > txqr.MaxChunkBytes+txqr.MaxFrameOverhead+1 {
			err = errors.New("invalid frame geometry")
		} else {
			if s.renderFn != nil {
				png = s.renderFn(sess.mode, sess.frames[index], sess.ppm, sess.modules)
			} else {
				png = renderByMode(sess.mode, sess.frames[index], sess.ppm, sess.modules)
			}
			if png == nil {
				err = errors.New("二维码生成失败")
			}
		}
		<-s.renderSlots
	case <-ctx.Done():
		err = ctx.Err()
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		err = ctx.Err()
		png = nil
	}
	if s.sessions[id] != sess {
		err = errMissingSession
		png = nil
	}
	if err == nil {
		for sess.pngBytes+int64(len(png)) > maxPNGCacheBytes && sess.evictPNG() {
		}
		for s.retainedLocked()+int64(len(png)) > maxSessionBytes && s.evictAnyLocked() {
		}
		if sess.pngBytes+int64(len(png)) <= maxPNGCacheBytes && s.retainedLocked()+int64(len(png)) <= maxSessionBytes {
			sess.pngs[index] = png
			sess.pngBytes += int64(len(png))
			sess.pngOrder = append(sess.pngOrder, index)
		}
	}
	call.data, call.err = png, err
	delete(sess.pending, index)
	close(call.done)
	s.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errMissingSession) {
		s.note("error", "frame.render", map[string]any{"session": id, "frame": index, "elapsedMs": time.Since(start).Milliseconds(), "error": err.Error()})
	} else if time.Since(start) > 200*time.Millisecond {
		s.note("warn", "frame.slow", map[string]any{"session": id, "frame": index, "elapsedMs": time.Since(start).Milliseconds()})
	}
	return png, err
}

func (s *Server) prefetch(id string, after int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil || s.stopped {
		return
	}
	needed := false
	for offset := 1; offset <= min(prefetchFrames, len(sess.frames)-1); offset++ {
		if sess.pngs[(after+offset)%len(sess.frames)] == nil {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	if sess.prefetchContext == nil || sess.prefetchContext.Err() != nil {
		ctx, cancel := context.WithCancel(context.Background())
		sess.prefetchContext, sess.prefetchCancel = ctx, cancel
		sess.prefetchJobs = make(chan int, prefetchFrames)
		sess.prefetchQueued = make(map[int]bool)
		jobs, queued := sess.prefetchJobs, sess.prefetchQueued
		for i := 0; i < 2; i++ {
			s.prefetchWG.Add(1)
			go func() {
				defer s.prefetchWG.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case index := <-jobs:
						_, _ = s.framePNG(ctx, id, index)
						s.mu.Lock()
						delete(queued, index)
						s.mu.Unlock()
					}
				}
			}()
		}
	}
	for offset := 1; offset <= min(prefetchFrames, len(sess.frames)-1); offset++ {
		index := (after + offset) % len(sess.frames)
		if sess.pngs[index] != nil || sess.pending[index] != nil || sess.prefetchQueued[index] {
			continue
		}
		select {
		case sess.prefetchJobs <- index:
			sess.prefetchQueued[index] = true
		default:
			return
		}
	}
}

func (s *Server) admitLocked(sess *session) error {
	sess.initCache()
	for s.retainedLocked()+sess.rawBytes+sess.pngBytes > maxSessionBytes && s.evictAnyLocked() {
	}
	if s.retainedLocked()+sess.rawBytes+sess.pngBytes > maxSessionBytes {
		return fmt.Errorf("会话缓存不足，请取消旧会话后重试")
	}
	return nil
}
