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


type testCBBInstance struct {
	n                   int
	t                   int
	kappa               int
	round               int
	epsilon             int
	committee           map[int]bool
	bbNodeChans         []chan *broadcastMessage
	cbbNodeChans        []chan *committeeBroadcastMessage
	senderChans         []chan []byte
	outs                []chan []byte
	committeeBroadcasts []*CommitteeBroadcast
}

func setupKeys(n int) (tcrsa.KeyShareList, *tcrsa.KeyMeta) {
	keyShares, keyMeta, err := tcrsa.NewKey(512, uint16(n/2+1), uint16(n), nil)
	if err != nil {
		panic(err)
	}
	return keyShares, keyMeta
}

func NewTestCBBInstance(n, ts, sender, kappa, round int, committee map[int]bool, keyShares tcrsa.KeyShareList, keyMeta *tcrsa.KeyMeta) *testCBBInstance {
	testCBBInstance := &testCBBInstance{
		n:                   n,
		t:                   ts,
		kappa:               kappa,
		round:               round,
		epsilon:             0,
		committee:           committee,
		bbNodeChans:         make([]chan *broadcastMessage, n),
		cbbNodeChans:        make([]chan *committeeBroadcastMessage, n),
		senderChans:         make([]chan []byte, n),
		outs:                make([]chan []byte, n),
		committeeBroadcasts: make([]*CommitteeBroadcast, n),
	}
	nodeChans := make(map[int]map[int][]chan *broadcastMessage) // maps round -> instance -> chans
	var mut sync.Mutex

	bbMulticastFunc := func(instance, round int, msg *broadcastMessage) {
		// If channels for round or instance don't exist create them first
		mut.Lock()
		if nodeChans[round] == nil {
			nodeChans[round] = make(map[int][]chan *broadcastMessage)
		}
		if len(nodeChans[round][instance]) != n {
			nodeChans[round][instance] = make([]chan *broadcastMessage, n)
			for i := 0; i < n; i++ {
				nodeChans[round][instance][i] = make(chan *broadcastMessage, 99*n)
			}
		}
		mut.Unlock()
		for _, node := range nodeChans[round][instance] {
			node <- msg
		}
	}
	cbbMulticastFunc := func(msg *committeeBroadcastMessage) {
		for _, node := range testCBBInstance.cbbNodeChans {
			node <- msg
		}
	}
	cbbSenderFunc := func(val []byte) {
		for _, node := range testCBBInstance.senderChans {
			node <- val
		}
	}

	for i := 0; i < n; i++ {
		hash := sha256.Sum256([]byte(strconv.Itoa(i)))
		hashPadded, _ := tcrsa.PrepareDocumentHash(keyMeta.PublicKey.Size(), crypto.SHA256, hash[:])
		// Dealer signs
		sig, _ := keyShares[len(keyShares)-1].Sign(hashPadded, crypto.SHA256, keyMeta)
		sigScheme := &signatureScheme{
			sig:     sig,
			keyMeta: keyMeta,
		}
		bbReceiveFunc := func(instance, round int) chan *broadcastMessage {
			// If channels for round or instance don't exist create them first
			mut.Lock()
			if nodeChans[round] == nil {
				nodeChans[round] = make(map[int][]chan *broadcastMessage)
			}
			if len(nodeChans[round][instance]) != n {
				nodeChans[round][instance] = make([]chan *broadcastMessage, n)
				for k := 0; k < n; k++ {
					nodeChans[round][instance][k] = make(chan *broadcastMessage, 99*n)
				}
			}
			mut.Unlock()
			return nodeChans[round][instance][i]
		}
		cbbReceiveFunc := func(index int) func() chan *committeeBroadcastMessage {
			return func() chan *committeeBroadcastMessage {
				return testCBBInstance.cbbNodeChans[index]
			}
		}
		cbbReceiveSenderVal := func(index int) func() chan []byte {
			return func() chan []byte {
				return testCBBInstance.senderChans[index]
			}
		}
		testCBBInstance.bbNodeChans[i] = make(chan *broadcastMessage, 100*n)
		testCBBInstance.cbbNodeChans[i] = make(chan *committeeBroadcastMessage, 100*n)
		testCBBInstance.senderChans[i] = make(chan []byte, 100*n)
		testCBBInstance.outs[i] = make(chan []byte, 100*n)
		testCBBInstance.committeeBroadcasts[i] = NewCommitteeBroadcast(n, i, ts, testCBBInstance.round, kappa, sender, 0, committee, testCBBInstance.outs[i], sigScheme, bbMulticastFunc, bbReceiveFunc, cbbSenderFunc, cbbMulticastFunc, cbbReceiveFunc(i), cbbReceiveSenderVal(i))
	}

	return testCBBInstance
}

func TestCBBEveryoneAgreesOnInput(t *testing.T) {
	committee := make(map[int]bool)
	committee[0] = true
	committee[1] = true
	committee[2] = true

	keyShares, keyMeta := setupKeys(11)
	testCBB := NewTestCBBInstance(10, 2, 0, 3, 0, committee, keyShares, keyMeta)
	var wg sync.WaitGroup

	inp := []byte("foo")
	testCBB.committeeBroadcasts[0].SetValue(inp)

	start := time.Now()
	for i := 0; i < testCBB.n-testCBB.t; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			testCBB.committeeBroadcasts[i].run()
		}()
	}
	wg.Wait()
	fmt.Println("Execution time:", time.Since(start))

	for i := 0; i < testCBB.n-testCBB.t; i++ {
		val := <-testCBB.outs[i]
		if !bytes.Equal(inp, val) {
			t.Errorf("Node %d returned %q, expected %q", i, val, inp)
		}
	}
}

func TestCBBDifferentRound(t *testing.T) {
	committee := make(map[int]bool)
	committee[0] = true
	committee[1] = true
	committee[2] = true

	keyShares, keyMeta := setupKeys(11)
	testCBB0 := NewTestCBBInstance(10, 0, 0, 3, 0, committee, keyShares, keyMeta)
	testCBB1 := NewTestCBBInstance(10, 2, 1, 3, 1, committee, keyShares, keyMeta)

	var wg sync.WaitGroup

	inp := []byte("foo")
	testCBB0.committeeBroadcasts[0].SetValue(inp)

	start := time.Now()

	for i := 0; i < testCBB0.n-testCBB0.t; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			testCBB0.committeeBroadcasts[i].run()
		}()
	}

	out := <-testCBB0.outs[0]
	if !bytes.Equal(inp, out) {
		t.Errorf("Got wrong output, expected %q, got %q", inp, out)
	}
	testCBB1.committeeBroadcasts[1].SetValue(out)

	for i := 0; i < testCBB1.n-testCBB1.t; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			testCBB1.committeeBroadcasts[i].run()
		}()
	}
	wg.Wait()
	fmt.Println("Execution time:", time.Since(start))

	for i := 0; i < testCBB1.n-testCBB1.t; i++ {
		val := <-testCBB1.outs[i]
		if !bytes.Equal(inp, val) {
			t.Errorf("expected %q, got %q from node %q", inp, val, i)
		}
	}
}

func TestCBBParallelWithDifferentSenders(t *testing.T) {
	// Scenario: 3 honest nodes, everyone has different input values, everyone runs 3 instances
	// of broadcast with 1 instance as sender.
	n := 3
	committee := map[int]bool{1: true}
	inp := [3][]byte{[]byte("one"), []byte("two"), []byte("three")}
	keyShares, keyMeta := setupKeys(4)
	var wg sync.WaitGroup

	testInstances := make(map[int]*testCBBInstance)
	for i := 0; i < n; i++ {
		testInstances[i] = NewTestCBBInstance(n, 0, i, 1, 0, committee, keyShares, keyMeta)
		testInstances[i].committeeBroadcasts[i].SetValue(inp[i])
	}

	start := time.Now()
	// Everyone runs 3 broadcast instances, 1 as sender
	for i := 0; i < n; i++ {
		i := i
		for j := 0; j < n; j++ {
			wg.Add(1)
			j := j
			go func() {
				defer wg.Done()
				testInstances[i].committeeBroadcasts[j].run()
			}()
		}
	}
	wg.Wait()
	fmt.Println("Execution time:", time.Since(start))

	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			val := <-testInstances[i].outs[j]
			if !bytes.Equal(val, inp[i]) {
				t.Errorf("Expected %q, got %q", inp[i], val)
			}
		}
	}
}
