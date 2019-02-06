package amp

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mnu5/svckit/log"
)

// Message types
const (
	Publish uint8 = iota
	Subscribe
	Request
	Response
	Ping
	Pong
	Alive
)

// Topic update types
const (
	Diff   uint8 = iota // merge into topic
	Full                // replace entire topic
	Append              // append to the end of the topic
	Update              // replace existing topic entry
	Close               // last message for the topic, topic is closed after this
)

// Replay types
const (
	Original uint8 = iota // original message
	Replay                // replay of the previously sent message
)

// supported compression types
const (
	CompressionNone uint8 = iota
	CompressionDeflate
)

var (
	compressionLenLimit = 8 * 1024 // do not compress messages smaller than
	separtor            = []byte{10}
)

// Subscriber is the interface for subscribing to the topics
type Subscriber interface {
	Send(m *Msg)
}

// BodyMarshaler nesto sto se zna zapakovati
type BodyMarshaler interface {
	MarshalJSON() ([]byte, error)
}

// Msg basic application message structure
type Msg struct {
	Type          uint8            `json:"t,omitempty"` // message type
	ReplyTo       string           `json:"r,omitempty"` // topic to send replay to
	CorrelationID uint64           `json:"i,omitempty"` // correlationID between request and response
	Error         string           `json:"e,omitempty"` // error description in response message
	ErrorCode     int64            `json:"c,omitempty"` // error code in response message
	URI           string           `json:"u,omitempty"` // has structure: topic/path
	Ts            int64            `json:"s,omitempty"` // timestamp unix milli
	UpdateType    uint8            `json:"p,omitempty"` // explains how to handle publish message
	Replay        uint8            `json:"l,omitempty"` // is this a re-play message (repeated)
	Subscriptions map[string]int64 `json:"b,omitempty"` // topics to subscribe to

	body          []byte
	noCompression bool
	payloads      map[uint8][]byte
	src           BodyMarshaler
	topic         string
	path          string

	sync.Mutex
}

// Parse decodes Msg from []byte
func Parse(buf []byte) *Msg {
	parts := bytes.SplitN(buf, separtor, 2)
	m := &Msg{}
	if err := json.Unmarshal(parts[0], m); err != nil {
		log.S("header", string(parts[0])).Error(err)
		return nil
	}
	if len(parts) > 1 {
		m.body = parts[1]
	}
	return m
}

// Undeflate enodes ws deflated message
func Undeflate(data []byte) []byte {
	buf := bytes.NewBuffer(data)
	buf.Write([]byte{0x00, 0x00, 0xff, 0xff})
	r := flate.NewReader(buf)
	defer r.Close()
	out := bytes.NewBuffer(nil)
	io.Copy(out, r)
	return out.Bytes()
}

// Marshal packs message for sending on the wire
func (m *Msg) Marshal() []byte {
	buf, _ := m.marshal(CompressionNone)
	return buf
}

// MarshalDeflate packs and compress message
func (m *Msg) MarshalDeflate() ([]byte, bool) {
	return m.marshal(CompressionDeflate)
}

// marshal encodes message into []byte
func (m *Msg) marshal(supportedCompression uint8) ([]byte, bool) {
	m.Lock()
	defer m.Unlock()
	compression := supportedCompression
	if m.noCompression {
		compression = CompressionNone
	}
	// check if we already have payload
	key := payloadKey(compression)
	if payload, ok := m.payloads[key]; ok {
		return payload, compression != CompressionNone
	}

	payload := m.payload()
	// decide wather we need compression
	if len(payload) < compressionLenLimit {
		m.noCompression = true
		compression = CompressionNone
	}
	// compress
	if compression == CompressionDeflate {
		payload = deflate(payload)
	}
	// store payload
	if m.payloads == nil {
		m.payloads = make(map[uint8][]byte)
	}
	m.payloads[key] = payload

	return payload, compression != CompressionNone
}

func (m *Msg) payload() []byte {
	header, _ := json.Marshal(m)
	buf := bytes.NewBuffer(header)
	buf.Write(separtor)
	if m.body != nil {
		buf.Write(m.body)
	}
	if m.src != nil {
		body, _ := m.src.MarshalJSON()
		buf.Write(body)
	}
	return buf.Bytes()
}

func payloadKey(compression uint8) uint8 {
	return compression
}

func deflate(src []byte) []byte {
	dest := bytes.NewBuffer(nil)
	c, _ := flate.NewWriter(dest, flate.DefaultCompression)
	c.Write(src)
	c.Close()
	buf := dest.Bytes()
	if len(buf) > 4 {
		return buf[0 : len(buf)-4]
	}
	return buf
}

// BodyTo unmarshals message body to the v
func (m *Msg) BodyTo(v interface{}) error {
	return json.Unmarshal(m.body, v)
}

// Unmarshal unmarshals message body to the v
func (m *Msg) Unmarshal(v interface{}) error {
	return json.Unmarshal(m.body, v)
}

// Response creates response message from original request
func (m *Msg) Response(b BodyMarshaler) *Msg {
	return &Msg{
		Type:          Response,
		CorrelationID: m.CorrelationID,
		src:           b,
	}
}

// ResponseTransportError creates response message with error set to transport error
func (m *Msg) ResponseTransportError() *Msg {
	return &Msg{
		Type:          Response,
		CorrelationID: m.CorrelationID,
		Error:         "transport error", // TODO
		ErrorCode:     -128,
	}
}

// Request creates request type message from original message
func (m *Msg) Request() *Msg {
	return &Msg{
		Type:          Request,
		CorrelationID: m.CorrelationID,
		URI:           m.URI,
		src:           m.src,
		body:          m.body,
	}
}

// NewAlive creates new alive type message
func NewAlive() *Msg {
	return &Msg{Type: Alive}
}

// NewPong creates new pong type message
func NewPong() *Msg {
	return &Msg{Type: Pong}
}

// IsPing returns true is message is Ping type
func (m *Msg) IsPing() bool {
	return m.Type == Ping
}

// IsAlive returns true is message is Alive type
func (m *Msg) IsAlive() bool {
	return m.Type == Alive
}

// NewPublish creates new publish type message
// Topic and path are combined in URI: topic/path
func NewPublish(topic, path string, ts int64, updateType uint8, o interface{}) *Msg {
	var b BodyMarshaler
	if t, ok := o.(BodyMarshaler); ok {
		b = t
	} else {
		b = JSONMarshaler(o)
	}

	uri := topic
	if path != "" {
		uri = topic + "/" + path
	}

	return &Msg{
		Type:       Publish,
		URI:        uri,
		Ts:         ts,
		UpdateType: updateType,
		topic:      topic,
		path:       path,
		src:        b,
	}
}

// IsTopicClose ...
func (m *Msg) IsTopicClose() bool {
	return m.UpdateType == Close
}

// IsReplay ...
func (m *Msg) IsReplay() bool {
	return m.Replay == Replay
}

// IsFull ...
func (m *Msg) IsFull() bool {
	return m.UpdateType == Full
}

// Topic returns topic part of the URI
func (m *Msg) Topic() string {
	if m.topic == "" {
		m.topic = m.URI
		if strings.Contains(m.URI, "/") {
			m.topic = strings.Split(m.URI, "/")[0]
		}
	}
	return m.topic
}

// Path returns path part of the URI
func (m *Msg) Path() string {
	if strings.Contains(m.URI, "/") {
		p := strings.SplitN(m.URI, "/", 2)
		if len(p) > 1 {
			return p[1]
		}
	}
	return ""
}

// AsReplay marks message as replay
func (m *Msg) AsReplay() *Msg {
	return &Msg{
		Type:       m.Type,
		URI:        m.URI,
		UpdateType: m.UpdateType,
		Replay:     Replay,
		Ts:         m.Ts,
		body:       m.body,
		src:        m.src,
	}
}

type jsonMarshaler struct {
	o interface{}
}

func (j jsonMarshaler) MarshalJSON() ([]byte, error) {
	if t, ok := j.o.(BodyMarshaler); ok {
		return t.MarshalJSON()
	}
	return json.Marshal(j.o)
}

// JSONMarshaler converst o to something which has MarshalJSON method
func JSONMarshaler(o interface{}) *jsonMarshaler {
	return &jsonMarshaler{o: o}
}

// TS return timestamp in unix milliseconds
func TS() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
