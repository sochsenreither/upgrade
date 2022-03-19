package binaryagreement

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"encoding/gob"
	"net"
	"time"

	"log"
	"strconv"

	"github.com/niclabs/tcrsa"
	"github.com/sochsenreither/upgrade/utils"
)

// TODO: network handler for coin
// TODO: network coin (doesn't use channels but ip addresses)

type CommonCoin struct {
	N           int                     // Number of nodes
	KeyMeta     *tcrsa.KeyMeta          // PKI
	RequestChan chan *utils.CoinRequest // Channel to receive requests
	Answer      func(req *utils.CoinRequest, val byte)
	Listener    func(requestChan chan *utils.CoinRequest)
}

func NewLocalCommonCoin(n int, keyMeta *tcrsa.KeyMeta, requestChannel chan *utils.CoinRequest) *CommonCoin {
	answer := func(req *utils.CoinRequest, val byte) {
		req.AnswerLocal <- val
	}
	coin := &CommonCoin{
		N:           n,
		KeyMeta:     keyMeta,
		RequestChan: requestChannel,
		Answer:      answer,
	}
	return coin
}

func NewNetworkCommonCoin(n int, keyMeta *tcrsa.KeyMeta, ips map[int]string) *CommonCoin {
	ownIP := ips[-1]
	requestChan := make(chan *utils.CoinRequest, 9999)

	// Listens to incoming request on the network
	listener := func(requestChan chan *utils.CoinRequest) {
		log.Printf("Common coin starting to listen to port %s", ownIP)
		l, err := net.Listen("tcp", ownIP)
		if err != nil {
			log.Fatalf("Coin wasn't able to start a listener. %s", err)
		}

		for {
			c, err := l.Accept()
			if err != nil {
				log.Printf("Coin wasn't able to accept connection. %s", err)
				continue
			}
			data := make([]byte, 1000)
			c.Read(data)
			buf := bytes.NewBuffer(data)
			coinRequest := new(utils.CoinRequest)
			dec := gob.NewDecoder(buf)
			err = dec.Decode(coinRequest)
			if err != nil {
				log.Printf("Coin wasn't able to decode message. %s", err)
				continue
			}
			//log.Printf("Coin received request. Sender: %d UROUND: %d Round: %d", coinRequest.Sender, coinRequest.UROUND, coinRequest.Round)
			requestChan <- coinRequest
		}
	}

	answer := func(req *utils.CoinRequest, val byte) {
		receiverIP := ips[req.Sender]
		c, err := net.Dial("tcp", receiverIP)
		for err != nil {
			time.Sleep(200 * time.Millisecond)
			c, err = net.Dial("tcp", receiverIP)
		}
		// Sender will be the value
		msg := &utils.Message{
			Sender:  int(val),
			Payload: nil,
		}
		handlerMsg := &utils.HandlerMessage{
			UROUND:   req.UROUND,
			Round:    req.Round,
			Instance: req.Instance,
			Origin:   utils.COIN,
			Payload:  msg,
		}
		buf := new(bytes.Buffer)
		enc := gob.NewEncoder(buf)
		err = enc.Encode(handlerMsg)
		if err != nil {
			log.Printf("Coin wasn't able to encode message. %s", err)
		}
		c.Write(buf.Bytes())
		// for err != nil {
		// 	time.Sleep(200 * time.Millisecond)
		// 	_, err = c.Write([]byte{val})
		// }
	}

	coin := &CommonCoin{
		N:           n,
		KeyMeta:     keyMeta,
		RequestChan: requestChan,
		Answer:      answer,
		Listener:    listener,
	}
	return coin
}

func (cc *CommonCoin) Run() {
	if cc.Listener != nil {
		go cc.Listener(cc.RequestChan)
	}
	// Maps from UROUND -> round -> instance -> nodeId
	received := make(map[int]map[int]map[int]map[int]*utils.CoinRequest)
	alreadySent := make(map[int]map[int]map[int]bool)
	coinVals := make(map[int]map[int]byte)

	for request := range cc.RequestChan {
		sender := request.Sender
		UROUND := request.UROUND
		round := request.Round
		instance := request.Instance

		if received[UROUND] == nil {
			received[UROUND] = make(map[int]map[int]map[int]*utils.CoinRequest)
		}
		if alreadySent[UROUND] == nil {
			alreadySent[UROUND] = make(map[int]map[int]bool)
		}
		if coinVals[UROUND] == nil {
			coinVals[UROUND] = make(map[int]byte)
		}
		// Create a new map the first time a request from a new round comes in
		if received[UROUND][round] == nil {
			received[UROUND][round] = make(map[int]map[int]*utils.CoinRequest)
		}
		if alreadySent[UROUND][round] == nil {
			alreadySent[UROUND][round] = make(map[int]bool)
		}

		if received[UROUND][round][instance] == nil {
			received[UROUND][round][instance] = make(map[int]*utils.CoinRequest)
		}
		received[UROUND][round][instance][sender] = request

		// Hash the round number
		h := sha256.Sum256([]byte(strconv.Itoa(round)))
		hash, err := tcrsa.PrepareDocumentHash(cc.KeyMeta.PublicKey.Size(), crypto.SHA256, h[:])
		if err != nil {
			log.Println("Common coin failed to create hash for round", round, err)
		}

		// Verify if the received signature share is valid
		if err := request.Sig.Verify(hash, cc.KeyMeta); err != nil {
			log.Printf("Common coin couldn't verify signature share from node %d. %s", sender, err)
			continue
		}

		// If enough signature shares were received for a given round combine them to a certificate
		if len(received[UROUND][round][instance]) >= cc.N/2+1 {
			if alreadySent[UROUND][round][instance] {
				// If the coin was already created and multicasted and if some node asks for the value at a later time, send the value only to this node
				go cc.Answer(request, coinVals[UROUND][round])
			} else {
				// Combine all received signature shares to a certificate
				// log.Println("Creating certificate in round", round)
				var sigShares tcrsa.SigShareList
				for _, req := range received[UROUND][round][instance] {
					sigShares = append(sigShares, req.Sig)
				}
				certificate, err := sigShares.Join(hash, cc.KeyMeta)
				if err != nil {
					log.Println("Common coin failed to create a certificate for round", round)
					continue
				}

				// Compute the hash of the certificate, take the least significant bit and use that as coin.
				certHash := sha256.Sum256(certificate)
				lsb := certHash[len(certHash)-1] & 0x01

				for _, req := range received[UROUND][round][instance] {
					go cc.Answer(req, lsb)
				}
				alreadySent[UROUND][round][instance] = true
				coinVals[UROUND][round] = lsb
			}
		}
	}
}
