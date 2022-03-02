package blockagreement

import (
	"crypto"
	"math/rand"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/niclabs/tcrsa"
	"github.com/sochsenreither/upgrade/utils"
)

type testGradedConsensusInstance struct {
	n               int
	ts              int
	round           int
	nodeChans       map[int][]chan *utils.Message
	tickers         []chan int
	gcs             []*gradedConsensus
	thresholdCrypto []*thresholdCrypto
	leaderChan      chan *leaderRequest
}

func newTestGradedConsensusInstance(n, ts, round int) *testGradedConsensusInstance {
	gc := &testGradedConsensusInstance{
		n:               n,
		ts:              ts,
		round:           round,
		nodeChans:       make(map[int][]chan *utils.Message),
		tickers:         make([]chan int, n),
		gcs:             make([]*gradedConsensus, n),
		thresholdCrypto: make([]*thresholdCrypto, n),
		leaderChan:      make(chan *leaderRequest, n),
	}

	keyShares, keyMeta, err := tcrsa.NewKey(512, uint16(n/2+1), uint16(n), nil)
	if err != nil {
		panic(err)
	}

	// Fill pre-block with enough valid messages
	pre := utils.NewPreBlock(n)
	for i := 0; i < n; i++ {
		// Create a test message with a corresponding signature by node i
		message := []byte("test")
		messageHash := sha256.Sum256(message)
		messageHashPadded, _ := tcrsa.PrepareDocumentHash(keyMeta.PublicKey.Size(), crypto.SHA256, messageHash[:])
		sig, _ := keyShares[i].Sign(messageHashPadded, crypto.SHA256, keyMeta)

		preMes := &utils.PreBlockMessage{
			Message: message,
			Sig:     sig,
		}
		pre.AddMessage(i, preMes)
	}

	var mu sync.Mutex
	multicast := func(id, round int, msg *utils.Message, params ...int) {
		go func() {
			var chans []chan *utils.Message
			mu.Lock()
			if gc.nodeChans[round] == nil {
				gc.nodeChans[round] = make([]chan *utils.Message, n)
				for i := 0; i < n; i++ {
					gc.nodeChans[round][i] = make(chan *utils.Message, 9999*n)
				}
			}
			// Set channels to send to to different variable in order to prevent data/lock races
			chans = append(chans, gc.nodeChans[round]...)
			mu.Unlock()
			if len(params) == 1 {
				chans[params[0]] <- msg
			} else {
				for i := 0; i < n; i++ {
					chans[i] <- msg
				}
			}
		}()
	}

	receive := func(id, round int) chan *utils.Message {
		mu.Lock()
		if gc.nodeChans[round] == nil {
			gc.nodeChans[round] = make([]chan *utils.Message, n)
			for i := 0; i < n; i++ {
				gc.nodeChans[round][i] = make(chan *utils.Message, 9999*n)
			}
		}
		ch := gc.nodeChans[round][id]
		mu.Unlock()
		return ch
	}


	// TODO: change to real sig
	h := pre.Hash()
	blockPointer := utils.NewBlockPointer(h[:], []byte{0})
	blockShare := utils.NewBlockShare(pre, blockPointer)

	// Set up individual graded consensus protocols
	for i := 0; i < n; i++ {
		vote := &vote{
			round:    0,
			blockShare: blockShare,
			commits:  nil,
		}
		gc.tickers[i] = make(chan int, n*n*n)
		gc.thresholdCrypto[i] = &thresholdCrypto{
			keyShare: keyShares[i],
			keyMeta:  keyMeta,
		}
		gc.gcs[i] = NewGradedConsensus(n, i, ts, round, gc.tickers[i], vote, gc.thresholdCrypto[i], gc.leaderChan, multicast, receive)
	}

	return gc
}

func TestGCEveryoneAgreesOnSameOutputInRoundOneWithGrade2(t *testing.T) {
	n := 4
	testGC := newTestGradedConsensusInstance(n, 1, 0)

	go testLeader(n, testGC.leaderChan)
	go tickr(testGC.tickers, 25*time.Millisecond, 10)
	for i := 0; i < testGC.n-testGC.ts; i++ {
		go testGC.gcs[i].run()
	}
	start := time.Now()

	for i := 0; i < testGC.n-testGC.ts; i++ {
		grade := testGC.gcs[i].GetValue()
		if grade.grade != 2 {
			t.Errorf("Node %d got grade %d, expected %d", i, grade.grade, 2)
		}
		if len(grade.commits) < testGC.n-testGC.ts {
			t.Errorf("Set of commits is too small, got %d, expected %d", len(grade.commits), 2)
		}
	}
	fmt.Println("Execution time:", time.Since(start))
}

func TestGCFailedRoundButStillTerminates(t *testing.T) {
	n := 4
	test := newTestGradedConsensusInstance(n, 1, 0)
	timeout := time.After(200 * time.Millisecond)
	done := make(chan struct{})

	helper := func() {
		start := time.Now()
		var wg sync.WaitGroup
		// Start protocol with only 1 honest node
		for i := 0; i < 1; i++ {
			wg.Add(1)
			i := i
			go func() {
				defer wg.Done()
				test.gcs[i].run()
			}()
		}
		go testLeader(n, test.leaderChan)
		go tickr(test.tickers, 25*time.Millisecond, 6)
		wg.Wait()
		fmt.Println("Execution time:", time.Since(start))
	}

	go func() {
		helper()
		done <- struct{}{}
	}()

	select {
	case <-timeout:
		t.Errorf("Protocol didn't terminate")
	case <-done:
	}
}

// Dummy leader who just responds with 0
func testLeader(n int, in chan *leaderRequest) {
	num := rand.Intn(n)
	for request := range in {
		answer := &leaderAnswer{
			round:  request.round,
			leader: num,
		}
		request.answer <- answer
	}
}
