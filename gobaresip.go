package gobaresip

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
)

//ResponseMsg
type ResponseMsg struct {
	Response bool   `json:"response,omitempty"`
	Ok       bool   `json:"ok,omitempty"`
	Data     string `json:"data,omitempty"`
	Token    string `json:"token,omitempty"`
	RawJSON  []byte `json:"-"`
}

//EventMsg
type EventMsg struct {
	Event           bool   `json:"event,omitempty"`
	Type            string `json:"type,omitempty"`
	Class           string `json:"class,omitempty"`
	AccountAOR      string `json:"accountaor,omitempty"`
	Direction       string `json:"direction,omitempty"`
	PeerURI         string `json:"peeruri,omitempty"`
	PeerDisplayname string `json:"peerdisplayname,omitempty"`
	ID              string `json:"id,omitempty"`
	RemoteAudioDir  string `json:"remoteaudiodir,omitempty"`
	Param           string `json:"param,omitempty"`
	RawJSON         []byte `json:"-"`
}

type Baresip struct {
	userAgent      string
	ctrlAddr       string
	wsAddr         string
	configPath     string
	audioPath      string
	debug          bool
	ctrlConn       net.Conn
	ctrlConnAlive  uint32
	responseChan   chan ResponseMsg
	eventChan      chan EventMsg
	responseWsChan chan []byte
	eventWsChan    chan []byte
	ctrlStream     *reader
	autoCmd        ac
}

type ac struct {
	mux sync.RWMutex
	num map[string]int

	hangupGap uint32
}

func New(options ...func(*Baresip) error) (*Baresip, error) {
	b := &Baresip{
		responseChan: make(chan ResponseMsg, 100),
		eventChan:    make(chan EventMsg, 100),
	}

	if err := b.SetOption(options...); err != nil {
		return nil, err
	}

	if b.audioPath == "" {
		b.audioPath = "."
	}
	if b.configPath == "" {
		b.configPath = "."
	}
	if b.ctrlAddr == "" {
		b.ctrlAddr = "127.0.0.1:4444"
	}
	if b.userAgent == "" {
		b.userAgent = "go-baresip"
	}

	b.autoCmd.num = make(map[string]int)

	if b.wsAddr != "" {
		b.responseWsChan = make(chan []byte, 100)
		b.eventWsChan = make(chan []byte, 100)

		h := newWsHub(b)
		go h.run()

		http.HandleFunc("/", serveRoot)
		http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			serveWs(h, w, r)
		})
		go http.ListenAndServe(b.wsAddr, nil)
	}
	if err := b.connectCtrl(); err != nil {
		return b, err
	}

	// Simple solution for this https://github.com/baresip/baresip/issues/584
	//go b.keepActive()

	return b, nil
}

func (b *Baresip) connectCtrl() error {
	var err error
	b.ctrlConn, err = net.Dial("tcp", b.ctrlAddr)
	if err != nil {
		atomic.StoreUint32(&b.ctrlConnAlive, 0)
		return fmt.Errorf("%v: please make sure ctrl_tcp is enabled", err)
	}

	b.ctrlStream = newReader(b.ctrlConn)

	atomic.StoreUint32(&b.ctrlConnAlive, 1)
	return nil
}

func (b *Baresip) Read() {
	for {
		if atomic.LoadUint32(&b.ctrlConnAlive) == 0 {
			break
		}

		msg, err := b.ctrlStream.readNetstring()
		if err != nil {
			log.Println(err)
			break
		}

		if bytes.Contains(msg, []byte("\"event\":true")) {
			if bytes.Contains(msg, []byte(",end of file")) {
				msg = bytes.Replace(msg, []byte("AUDIO_ERROR"), []byte("AUDIO_EOF"), 1)
			}

			var e EventMsg
			e.RawJSON = msg

			err := json.Unmarshal(e.RawJSON, &e)
			if err != nil {
				log.Println(err, string(e.RawJSON))
				continue
			}

			b.eventChan <- e
			if b.wsAddr != "" {
				select {
				case b.eventWsChan <- e.RawJSON:
				default:
				}
			}
		} else if bytes.Contains(msg, []byte("\"response\":true")) {

			var r ResponseMsg
			r.RawJSON = msg

			err := json.Unmarshal(r.RawJSON, &r)
			if err != nil {
				log.Println(err, string(r.RawJSON))
				continue
			}

			if strings.HasPrefix(r.Token, "cmd_dial") {
				if d := atomic.LoadUint32(&b.autoCmd.hangupGap); d > 0 {
					if id := findID([]byte(r.Data)); len(id) > 1 {
						go func() {
							time.Sleep(time.Duration(d) * time.Second)
							b.CmdHangupID(id)
						}()
					}
				}
			}

			if strings.HasPrefix(r.Token, "cmd_auto") {
				r.Ok = true
				b.autoCmd.mux.RLock()
				r.Data = fmt.Sprintf("dial%v;hangupgap=%d",
					b.autoCmd.num,
					atomic.LoadUint32(&b.autoCmd.hangupGap),
				)
				b.autoCmd.mux.RUnlock()
				r.Data = strings.Replace(r.Data, " ", ",", -1)
				r.Data = strings.Replace(r.Data, ":", ";autodialgap=", -1)
				rj, err := json.Marshal(r)
				if err != nil {
					log.Println(err, r.Data)
					continue
				}
				r.RawJSON = rj
			}

			b.responseChan <- r
			if b.wsAddr != "" {
				select {
				case b.responseWsChan <- r.RawJSON:
				default:
				}
			}
		}
	}
}

func findID(data []byte) string {
	if posA := bytes.Index(data, []byte("call id: ")); posA > 0 {
		if posB := bytes.Index(data[posA:], []byte("\n")); posB > 0 {
			l := len("call id: ")
			return string(data[posA+l : posA+posB])
		}
	}
	return ""
}

func (b *Baresip) Close() {
	atomic.StoreUint32(&b.ctrlConnAlive, 0)
	if b.ctrlConn != nil {
		b.ctrlConn.Close()
	}
	close(b.responseChan)
	close(b.eventChan)
}

// GetEventChan returns the receive-only EventMsg channel for reading data.
func (b *Baresip) GetEventChan() <-chan EventMsg {
	return b.eventChan
}

// GetResponseChan returns the receive-only ResponseMsg channel for reading data.
func (b *Baresip) GetResponseChan() <-chan ResponseMsg {
	return b.responseChan
}

var ping = []byte(`16:{"token":"ping"},`)

func (b *Baresip) keepActive() {
	for {
		time.Sleep(1 * time.Second)
		if atomic.LoadUint32(&b.ctrlConnAlive) == 0 {
			break
		}
		b.ctrlConn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		b.ctrlConn.Write(ping)
	}
}

