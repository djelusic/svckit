package broker

import (
	"sync"
	"time"
)

var (
	brokers     map[string]*Broker
	brokersLock sync.RWMutex
	ttl         time.Duration = time.Hour
	defaultSize int           = 100
)

// SetTTL postavlja TTL za sve brokere
func SetTTL(newTTL time.Duration) {
	ttl = newTTL
}

func init() {
	brokers = make(map[string]*Broker)
}

// Message poruka full/diff brokera
type Message struct {
	Event string
	Data  []byte
}

// NewMessage kreira novi Message s podacima
func NewMessage(event string, data []byte) *Message {
	return &Message{
		Event: event,
		Data:  data,
	}
}

type state interface {
	put(*Message)
	get() *Message
	emit(chan *Message)
	waitTouch()
}

// Broker struktura full/diff ili buffered brokera
type Broker struct {
	topic       string
	state       state
	subscribers map[chan *Message]bool
	sync.RWMutex
	removeLock sync.RWMutex
	updated    time.Time
}

func newBroker(topic string) *Broker {
	return &Broker{
		topic:       topic,
		subscribers: make(map[chan *Message]bool),
		updated:     time.Now(),
	}
}

// NewBufferedBroker kreira novog buffered brokera
// - broker inicijalno ina buffer od 100 poruka (cuva ih kao full)
func NewBufferedBroker(topic string, size int) *Broker {
	b := newBroker(topic)
	b.state = newRingBuffer(size)
	return b
}

// NewFullDiffBroker  kreira novog full/diff brokera
// - broker ima samo 1 full
func NewFullDiffBroker(topic string) *Broker {
	b := newBroker(topic)
	b.state = newRingBuffer(1)
	return b
}

// State  vraca trenutni full
func (b *Broker) State() *Message {
	return b.state.get()
}

// activeSubscribers vraca kopiju aktivnih subscribera
func (b *Broker) activeSubscribers() map[chan *Message]bool {
	subs := make(map[chan *Message]bool)
	b.Lock()
	defer b.Unlock()
	for ch, fullSent := range b.subscribers {
		subs[ch] = fullSent
	}
	return subs
}

// removeSubscribers mice sve subscribere sa brokera
func (b *Broker) removeSubscribers() {
	subs := b.activeSubscribers()
	b.removeLock.Lock()
	defer b.removeLock.Unlock()
	for ch := range subs {
		b.Unsubscribe(ch)
	}
}

func (b *Broker) setSubscriber(ch chan *Message, sentFull bool) {
	b.Lock()
	defer b.Unlock()
	b.subscribers[ch] = sentFull
}

// Subscribe dodaje subscribera na brokera
// - vraca channel za poruke
// - salje full prije nego doda subscribera u listu za primanje diff-ova
func (b *Broker) Subscribe() chan *Message {
	// log.S("topic", b.topic).Debug("subscribe")
	ch := make(chan *Message)
	if b.state != nil {
		go func() {
			b.removeLock.RLock()
			defer b.removeLock.RUnlock()
			b.state.waitTouch()       // ceka barem jednu poruku u bufferu
			b.state.emit(ch)          // salje sve poruke u bufferu (fullove)
			b.setSubscriber(ch, true) // sad subscriber moze primati diffove
		}()
	}
	return ch
}

// Unsubscribe mice subscribera iz liste subscribera ako postoji
func (b *Broker) Unsubscribe(ch chan *Message) {
	b.Lock()
	defer b.Unlock()
	if _, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(ch)
	}
}

func (b *Broker) full(msg *Message) {
	b.Lock()
	defer b.Unlock()
	b.state.put(msg)
	b.updated = time.Now()
}

func (b *Broker) diff(msg *Message) {
	b.RLock()
	defer b.RUnlock()
	for c, sentFull := range b.subscribers {
		if sentFull {
			c <- msg
		}
	}
}

func (b *Broker) expired() bool {
	b.RLock()
	defer b.RUnlock()
	return b.updated.Before(time.Now().Add(-ttl))
}

// Full sprema full podatke za topic
func Full(topic, event string, data []byte) {
	msg := NewMessage(event, data)
	GetFullDiffBroker(topic).full(msg)
}

// Diff sprema diff za topic
func Diff(topic, event string, data []byte) {
	msg := NewMessage(event, data)
	GetFullDiffBroker(topic).diff(msg)
}

// Stream sprema full i diff za topic
// - ovo koristimo za streamanje logova gde na pocetku
// dobijemo X log linija kao full-ove i nastavljamo slusati diff-ove
func Stream(topic, event string, data []byte) {
	msg := NewMessage(event, data)
	b := GetBufferedBroker(topic)
	b.full(msg)
	b.diff(msg)
}

// FindBroker pronalazi brokera za topic
func FindBroker(topic string) (*Broker, bool) {
	brokersLock.RLock()
	brokersLock.RUnlock()
	b, ok := brokers[topic]
	return b, ok
}

func createFullDiffBroker(topic string) *Broker {
	brokersLock.Lock()
	defer brokersLock.Unlock()
	b := NewFullDiffBroker(topic)
	brokers[topic] = b
	return b
}

func createBufferedBroker(topic string, size int) *Broker {
	brokersLock.Lock()
	defer brokersLock.Unlock()
	b := NewBufferedBroker(topic, size)
	brokers[topic] = b
	return b
}

// GetFullDiffBroker dohvaca postojeceg ili kreira novi full/diff broker
func GetFullDiffBroker(topic string) *Broker {
	b, ok := FindBroker(topic)
	if !ok {
		return createFullDiffBroker(topic)
	}
	return b
}

// GetBufferedBroker dohvaca postojeceg ili kreira novi buffered broker
func GetBufferedBroker(topic string) *Broker {
	b, ok := FindBroker(topic)
	if !ok {
		return createBufferedBroker(topic, defaultSize)
	}
	return b
}

// CleanUpBrokers clisti listu brokera koji nisu dobili update
// - namjena periodicki pozivati da se ne gomilaju brokeri koji nista ne rade
func CleanUpBrokers() {
	brokersLock.Lock()
	defer brokersLock.Unlock()
	for topic, b := range brokers {
		if b.expired() {
			delete(brokers, topic) // obrisi brokera za topic
			b.removeSubscribers()  // makni njegove subscribere
		}
	}
}
