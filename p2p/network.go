//
// Copyright (c) 2020 Markku Rossi
//
// All rights reserved.
//

package p2p

import (
	"crypto/rsa"
	"fmt"
	"log"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/markkurossi/mpc/ot"
)

type Network struct {
	ID       int
	m        sync.Mutex
	Peers    map[int]*Peer
	addr     string
	listener net.Listener
}

func NewNetwork(addr string, id int) (*Network, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	nw := &Network{
		ID:       id,
		Peers:    make(map[int]*Peer),
		addr:     addr,
		listener: listener,
	}
	go nw.acceptLoop()
	return nw, nil
}

func (nw *Network) Close() error {
	return nw.listener.Close()
}

func (nw *Network) AddPeer(addr string, id int) error {
	// Try to connect to peer.
	for {
		// Check if we have already accepted peer `id`.
		nw.m.Lock()
		_, ok := nw.Peers[id]
		nw.m.Unlock()
		if ok {
			return nil
		}

		log.Printf("NW %d: Connecting to peer %d...\n", nw.ID, id)
		nc, err := net.Dial("tcp", addr)
		if err != nil {
			delay := 5 * time.Second
			log.Printf("NW %d: Connect to %s failed, retrying in %s\n",
				nw.ID, addr, delay)
			<-time.After(delay)
			continue
		}
		log.Printf("NW %d: Connected to %s\n", nw.ID, addr)
		conn := NewConn(nc)

		if err := conn.SendUint32(nw.ID); err != nil {
			conn.Close()
			return err
		}
		if err := conn.Flush(); err != nil {
			conn.Close()
			return err
		}
		if err := nw.newPeer(true, conn, id); err != nil {
			fmt.Printf("Failed to add peer: %s\n", err)
		}
	}
}

func (nw *Network) Ping() {
	for _, peer := range nw.Peers {
		peer.Ping()
	}
}

func (nw *Network) acceptLoop() {
	for {
		nc, err := nw.listener.Accept()
		if err != nil {
			log.Printf("NW %d: accept failed: %s\n", nw.ID, err)
			continue
		}
		conn := NewConn(nc)

		// Read peer ID.
		id, err := conn.ReceiveUint32()
		if err != nil {
			log.Printf("NW %d: I/O error: %s\n", nw.ID, err)
			conn.Close()
			continue
		}

		err = nw.newPeer(false, conn, id)
		if err != nil {
			log.Printf("inbound connection error: %s\n", err)
		}
	}
}

func (nw *Network) newPeer(client bool, conn *Conn, id int) error {
	nw.m.Lock()
	peer, ok := nw.Peers[id]
	if ok {
		nw.m.Unlock()
		log.Printf("NW %d: peer %d already connected\n", nw.ID, id)
		return conn.Close()
	}
	peer = &Peer{
		id:     id,
		conn:   conn,
		client: client,
	}
	nw.Peers[id] = peer
	nw.m.Unlock()

	return peer.init()
}

type Peer struct {
	id         int
	conn       *Conn
	client     bool
	otSender   *ot.Sender
	otReceiver *ot.Receiver
}

func (peer *Peer) Close() error {
	return peer.conn.Close()
}

func (peer *Peer) Ping() error {
	if err := peer.conn.SendUint32(0xffffffff); err != nil {
		return err
	}
	return peer.conn.Flush()
}

func (peer *Peer) init() error {
	fmt.Printf("peer %d: init\n", peer.id)

	// Read peer public key.
	finished := make(chan error)
	go func() {
		pubN, err := peer.conn.ReceiveData()
		if err != nil {
			finished <- err
			return
		}
		pubE, err := peer.conn.ReceiveUint32()
		if err != nil {
			finished <- err
			return
		}
		pub := &rsa.PublicKey{
			N: new(big.Int).SetBytes(pubN),
			E: pubE,
		}
		receiver, err := ot.NewReceiver(pub)
		if err != nil {
			finished <- err
			return
		}
		peer.otReceiver = receiver
		finished <- nil
	}()

	// Init oblivious transfer.
	sender, err := ot.NewSender(2048)
	if err != nil {
		<-finished
		return err
	}
	peer.otSender = sender

	// Send our public key to peer.
	pub := sender.PublicKey()
	data := pub.N.Bytes()
	if err := peer.conn.SendData(data); err != nil {
		<-finished
		return err
	}
	if err := peer.conn.SendUint32(pub.E); err != nil {
		<-finished
		return err
	}
	peer.conn.Flush()

	return <-finished
}

func (peer *Peer) OTLambda(count int, choices, x1, x2 *big.Int) (
	result *big.Int, err error) {

	var mode string
	if peer.client {
		mode = "OT-client"
	} else {
		mode = "OT-server"
	}

	fmt.Printf("%s for peer %d: count=%d\n", mode, peer.id, count)

	if peer.client {
		// Client queries first.
		result, err = peer.OTLambdaQuery(count, choices)
		if err != nil {
			return
		}

		// Serve peer queries.
		err = peer.OTLambdaRespond(count, x1, x2)
		if err != nil {
			return
		}
	} else {
		// Serve peer queries.
		err = peer.OTLambdaRespond(count, x1, x2)
		if err != nil {
			return
		}

		// Server queries second.
		result, err = peer.OTLambdaQuery(count, choices)
		if err != nil {
			return
		}
	}
	fmt.Printf("%s for peer %d done\n", mode, peer.id)
	return
}

func (peer *Peer) OTLambdaQuery(count int, choices *big.Int) (
	*big.Int, error) {

	// Number of OTs following
	if err := peer.conn.SendUint32(count); err != nil {
		return nil, err
	}
	if err := peer.conn.Flush(); err != nil {
		return nil, err
	}

	// OTs for each query.
	result := new(big.Int)
	for i := 0; i < count; i++ {
		fmt.Printf(" - peer %d: OT %d...\n", peer.id, i)
		n, err := peer.conn.Receive(peer.otReceiver, uint(i), choices.Bit(i))
		if err != nil {
			return nil, err
		}
		if len(n) != 1 {
			return nil, fmt.Errorf("invalid OT result of length %d", len(n))
		}
		if n[0] != 0 {
			result.SetBit(result, i, 1)
		}
	}
	return result, nil
}

func (peer *Peer) OTLambdaRespond(count int, x1, x2 *big.Int) error {
	pc, err := peer.conn.ReceiveUint32()
	if err != nil {
		fmt.Printf("respond0: %s\n", err)
		return err
	}
	if pc != count {
		fmt.Printf("respond1: %s\n", err)
		return fmt.Errorf("protocol error: peer count %d, our %d",
			pc, count)
	}
	for i := 0; i < count; i++ {
		bit, err := peer.conn.ReceiveUint32()
		if err != nil {
			fmt.Printf("respond2: %s\n", err)
			return err
		}
		var m0, m1 [1]byte

		if x1.Bit(bit) != 0 {
			m0[0] = 1
		}
		if x2.Bit(bit) != 0 {
			m1[0] = 1
		}

		xfer, err := peer.otSender.NewTransfer(m0[:], m1[:])
		if err != nil {
			fmt.Printf("respond3: %s\n", err)
			return err
		}
		x0, x1 := xfer.RandomMessages()
		if err := peer.conn.SendData(x0); err != nil {
			fmt.Printf("respond4: %s\n", err)
			return err
		}
		if err := peer.conn.SendData(x1); err != nil {
			fmt.Printf("respond5: %s\n", err)
			return err
		}
		if err := peer.conn.Flush(); err != nil {
			fmt.Printf("respond6: %s\n", err)
			return err
		}

		v, err := peer.conn.ReceiveData()
		if err != nil {
			fmt.Printf("respond7: %s\n", err)
			return err
		}
		xfer.ReceiveV(v)

		m0p, m1p, err := xfer.Messages()
		if err != nil {
			fmt.Printf("respond8: %s\n", err)
			return err
		}
		if err := peer.conn.SendData(m0p); err != nil {
			fmt.Printf("respond9: %s\n", err)
			return err
		}
		if err := peer.conn.SendData(m1p); err != nil {
			fmt.Printf("respond10: %s\n", err)
			return err
		}
		if err := peer.conn.Flush(); err != nil {
			fmt.Printf("respond11: %s\n", err)
			return err
		}
	}

	return nil
}

func (peer *Peer) OTR(choices *big.Int,
	x1Ag, x2Ag, x1Bg, x2Bg, x1Cg, x2Cg []ot.Label) (
	ra, rb, rc []ot.Label, err error) {
	err = fmt.Errorf("OKR not implemented yet")
	return
}