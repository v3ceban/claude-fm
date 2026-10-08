package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type MPV struct {
	cmd   *exec.Cmd
	Stdin io.WriteCloser
	sock  string
	conn  net.Conn
	rd    *bufio.Reader
	mu    sync.Mutex
	reqID int
	done  chan struct{}
	err   error
}

func startMPV(ao string, volume int) (*MPV, error) {
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("claude-fm-%d.sock", os.Getpid()))
	os.Remove(sock)
	args := []string{
		"--no-config", "--no-video", "--no-terminal", "--really-quiet",
		"--demuxer=rawaudio", "--demuxer-rawaudio-format=s16le",
		fmt.Sprintf("--demuxer-rawaudio-rate=%d", SampleRate), "--demuxer-rawaudio-channels=2",
		"--cache=yes", "--cache-pause=no", "--demuxer-readahead-secs=10", "--demuxer-max-bytes=32MiB", "--demuxer-max-back-bytes=2MiB",
		"--audio-buffer=0.3", "--idle=no", "--keep-open=no",
		"--input-ipc-server=" + sock,
		fmt.Sprintf("--volume=%d", volume),
	}
	if ao != "" {
		args = append(args, "--ao="+ao)
	}
	args = append(args, "fd://0")
	cmd := exec.Command("mpv", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mpv: %w", err)
	}
	m := &MPV{cmd: cmd, Stdin: stdin, sock: sock, done: make(chan struct{})}
	go func() {
		m.err = cmd.Wait()
		close(m.done)
	}()
	return m, nil
}

func (m *MPV) connect() error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		c, err := net.Dial("unix", m.sock)
		if err == nil {
			m.conn, m.rd = c, bufio.NewReader(c)
			return nil
		}
		select {
		case <-m.done:
			return fmt.Errorf("mpv exited before IPC came up: %v", m.err)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mpv ipc: %w", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

type mpvResp struct {
	Data      json.RawMessage `json:"data"`
	Error     string          `json:"error"`
	RequestID int             `json:"request_id"`
}

func (m *MPV) command(args ...any) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		if err := m.connect(); err != nil {
			return nil, err
		}
	}
	m.reqID++
	id := m.reqID
	msg, _ := json.Marshal(map[string]any{"command": args, "request_id": id})
	m.conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := m.conn.Write(append(msg, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := m.rd.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var r mpvResp
		if json.Unmarshal(line, &r) != nil || r.RequestID != id {
			continue
		}
		if r.Error != "success" {
			return nil, fmt.Errorf("mpv: %s", r.Error)
		}
		return r.Data, nil
	}
}

func getProp[T any](m *MPV, prop string) (T, bool) {
	var v T
	d, err := m.command("get_property", prop)
	if err != nil || len(d) == 0 || string(d) == "null" || json.Unmarshal(d, &v) != nil {
		return v, false
	}
	return v, true
}

func (m *MPV) GetFloat(prop string) (float64, bool) { return getProp[float64](m, prop) }
func (m *MPV) GetBool(prop string) (bool, bool)     { return getProp[bool](m, prop) }

func (m *MPV) Stop() {
	m.Stdin.Close()
	select {
	case <-m.done:
	case <-time.After(300 * time.Millisecond):
		m.cmd.Process.Kill()
		<-m.done
	}
	m.mu.Lock()
	if m.conn != nil {
		m.conn.Close()
	}
	m.mu.Unlock()
	os.Remove(m.sock)
}
