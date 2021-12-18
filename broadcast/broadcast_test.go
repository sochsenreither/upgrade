package broadcast

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/niclabs/tcrsa"
)

func setupKeys(n int) (tcrsa.KeyShareList, *tcrsa.KeyMeta) {
	keyShares, keyMeta, err := tcrsa.NewKey(512, uint16(n/2+1), uint16(n), nil)
	if err != nil {
		panic(err)
	}
	return keyShares, keyMeta
}

func TestBroadcastOneInstanceWithByzantineNode(t *testing.T) {
	n := 4
	ta := 1
	var wg sync.WaitGroup

	committee := make(map[int]bool)
	outs := make([]chan []byte, n)
	nodeChans := make(map[int]chan *message) // maps node -> message channel
	broadcasts := make([]*ReliableBroadcast, n)
	keyShares, keyMeta := setupKeys(n + 1)

	committee[0] = true
	committee[1] = true

	multicast := func(id, instance, round int, msg *message) {
		go func() {
			switch msg.paylaod.(type) {
			case *sMessage:
				for k, node := range nodeChans {
					if committee[k] {
						node <- msg
					}
				}
			default:
				for _, node := range nodeChans {
					node <- msg
				}
			}
		}()
	}
	receive := func(id, instance, round int) *message {
		val := <-nodeChans[id]
		return val
	}

	for i := 0; i < n-ta; i++ {
		// Dealer signs node id
		hash := sha256.Sum256([]byte(strconv.Itoa(i)))
		paddedHash, _ := tcrsa.PrepareDocumentHash(keyMeta.PublicKey.Size(), crypto.SHA256, hash[:])
		sig, _ := keyShares[len(keyShares)-1].Sign(paddedHash, crypto.SHA256, keyMeta)
		signature := &Signature{
			sig:     sig,
			keyMeta: keyMeta,
		}
		config := &ReliableBroadcastConfig{
			n:        n,
			nodeId:   i,
			t:        ta,
			kappa:    1,
			epsilon:  0,
			senderId: 0,
			round:    0,
		}
		outs[i] = make(chan []byte, 99)
		nodeChans[i] = make(chan *message, 999)
		broadcasts[i] = NewReliableBroadcast(config, committee, outs[i], signature, multicast, receive)
	}
	input := []byte("foo")
	broadcasts[0].SetValue(input)

	start := time.Now()
	wg.Add(n - ta)
	for i := 0; i < n-ta; i++ {
		i := i
		go func() {
			defer wg.Done()
			broadcasts[i].run()
		}()
	}
	wg.Wait()
	fmt.Println("Execution time:", time.Since(start))

	for i := 0; i < n-ta; i++ {
		val := <-outs[i]
		if !bytes.Equal(val, input) {
			t.Errorf("Expected %s, got %s from node %d", string(input), string(val), i)
		}
	}
}

func TestBroadcastParallelMultipleSendersOneRound(t *testing.T) {
	// Scenario: Four honest nodes, one byzantine . Every node has a different initial input value.
	// Every node runs four instances of broadcast, one instance as sender. Every broadcast run in
	// one instance should output the same value. (The last instance doesn't output anything, since
	// the sender is byzantine. The test should still terminate and every other instance should be
	// correct)
	n := 4
	ta := 1
	var wg sync.WaitGroup
	var mu sync.Mutex
	inputs := [4][]byte{[]byte("zero"), []byte("one"), []byte("two"), []byte("three")}
	outs := make(map[int][]chan []byte)
	nodeChans := make(map[int]map[int][]chan *message) // maps round -> instance -> chans
	broadcasts := make(map[int][]*ReliableBroadcast)
	keyShares, keyMeta := setupKeys(n + 1)
	committee := make(map[int]bool)
	committee[0] = true
	committee[1] = true

	multicast := func(id, instance, round int, msg *message) {
		go func() {
			// If channels for round or instance don't exist create them first
			mu.Lock()
			if nodeChans[round] == nil {
				nodeChans[round] = make(map[int][]chan *message)
			}
			if len(nodeChans[round][instance]) != n {
				nodeChans[round][instance] = make([]chan *message, n)
				for i := 0; i < n; i++ {
					nodeChans[round][instance][i] = make(chan *message, 999*n)
				}
			}
			mu.Unlock()

			switch msg.paylaod.(type) {
			case *sMessage:
				for i, node := range nodeChans[round][instance] {
					if committee[i] {
						node <- msg
					}
				}
			default:
				for _, node := range nodeChans[round][instance] {
					node <- msg
				}
			}
		}()
	}
	receive := func(id, instance, round int) *message {
		// If channels for round or instance don't exist create them first
		mu.Lock()
		if nodeChans[round] == nil {
			nodeChans[round] = make(map[int][]chan *message)
		}
		if len(nodeChans[round][instance]) != n {
			nodeChans[round][instance] = make([]chan *message, n)
			for k := 0; k < n; k++ {
				nodeChans[round][instance][k] = make(chan *message, 999*n)
			}
		}
		mu.Unlock()
		val := <-nodeChans[round][instance][id]
		return val
	}

	for i := 0; i < n-ta; i++ {
		// Dealer signs node id
		hash := sha256.Sum256([]byte(strconv.Itoa(i)))
		paddedHash, _ := tcrsa.PrepareDocumentHash(keyMeta.PublicKey.Size(), crypto.SHA256, hash[:])
		sig, _ := keyShares[len(keyShares)-1].Sign(paddedHash, crypto.SHA256, keyMeta)
		signature := &Signature{
			sig:     sig,
			keyMeta: keyMeta,
		}
		outs[i] = make([]chan []byte, n)
		for j := 0; j < n; j++ {
			config := &ReliableBroadcastConfig{
				n:        n,
				nodeId:   i,
				t:        ta,
				kappa:    1,
				epsilon:  0,
				senderId: j,
				round:    0,
			}
			outs[i][j] = make(chan []byte, 100)
			broadcasts[i] = append(broadcasts[i], NewReliableBroadcast(config, committee, outs[i][j], signature, multicast, receive))
			if i == j {
				broadcasts[i][j].SetValue(inputs[j])
			}
		}
	}
	start := time.Now()
	wg.Add((n-ta)*(n-ta))
	for i := 0; i < n-ta; i++ {
		i := i
		for j := 0; j < n; j++ {
			j := j
			go func() {
				defer wg.Done()
				broadcasts[i][j].run()
			}()
		}
	}
	wg.Wait()
	fmt.Println("Execution time:", time.Since(start))

	for i := 0; i < n-ta; i++ {
		for j := 0; j < n-ta; j++ {
			val := <- broadcasts[i][j].out
			if !bytes.Equal(val, inputs[broadcasts[i][j].senderId]) {
				t.Errorf("Expected %q, got %q", inputs[broadcasts[i][j].senderId], val)
			}
		}
	}
}
