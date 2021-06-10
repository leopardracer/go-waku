package filter

import (
	"context"
	"encoding/hex"
	"fmt"

	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	libp2pProtocol "github.com/libp2p/go-libp2p-core/protocol"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/status-im/go-waku/waku/v2/protocol"
	"github.com/status-im/go-waku/waku/v2/protocol/pb"
)

var log = logging.Logger("wakufilter")

type (
	ContentFilterChan chan *pb.WakuMessage

	Filter struct {
		ContentFilters []*pb.FilterRequest_ContentFilter
		Chan           ContentFilterChan
	}
	// @TODO MAYBE MORE INFO?
	Filters map[string]Filter

	Subscriber struct {
		peer      string
		requestId string
		filter    pb.FilterRequest // @TODO MAKE THIS A SEQUENCE AGAIN?
	}

	MessagePushHandler func(requestId string, msg pb.MessagePush)

	WakuFilter struct {
		ctx         context.Context
		h           host.Host
		subscribers []Subscriber
		pushHandler MessagePushHandler
		MsgC        chan *protocol.Envelope
	}
)

// NOTE This is just a start, the design of this protocol isn't done yet. It
// should be direct payload exchange (a la req-resp), not be coupled with the
// relay protocol.

const WakuFilterCodec = "/vac/waku/filter/2.0.0-beta1"

const WakuFilterProtocolId = libp2pProtocol.ID(WakuFilterCodec)

// Error types (metric label values)
const (
	dialFailure      = "dial_failure"
	decodeRpcFailure = "decode_rpc_failure"
)

func (filters *Filters) Notify(msg *pb.WakuMessage, requestId string) {
	for key, filter := range *filters {
		// We do this because the key for the filter is set to the requestId received from the filter protocol.
		// This means we do not need to check the content filter explicitly as all MessagePushs already contain
		// the requestId of the coresponding filter.
		if requestId != "" && requestId == key {
			filter.Chan <- msg
			continue
		}

		// TODO: In case of no topics we should either trigger here for all messages,
		// or we should not allow such filter to exist in the first place.
		for _, contentFilter := range filter.ContentFilters {
			if msg.ContentTopic == contentFilter.ContentTopic {
				filter.Chan <- msg
				break
			}
		}
	}
}

func (wf *WakuFilter) selectPeer() *peer.ID {
	// @TODO We need to be more stratigic about which peers we dial. Right now we just set one on the service.
	// Ideally depending on the query and our set  of peers we take a subset of ideal peers.
	// This will require us to check for various factors such as:
	//  - which topics they track
	//  - latency?
	//  - default store peer?

	// Selects the best peer for a given protocol
	var peers peer.IDSlice
	for _, peer := range wf.h.Peerstore().Peers() {
		protocols, err := wf.h.Peerstore().SupportsProtocols(peer, string(WakuFilterProtocolId))
		if err != nil {
			log.Error("error obtaining the protocols supported by peers", err)
			return nil
		}

		if len(protocols) > 0 {
			peers = append(peers, peer)
		}
	}

	if len(peers) >= 1 {
		// TODO: proper heuristic here that compares peer scores and selects "best" one. For now the first peer for the given protocol is returned
		return &peers[0]
	}

	return nil
}

func (wf *WakuFilter) onRequest(s network.Stream) {
	defer s.Close()

	filterRPCRequest := &pb.FilterRPC{}

	reader := protoio.NewDelimitedReader(s, 64*1024)

	err := reader.ReadMsg(filterRPCRequest)
	if err != nil {
		log.Error("error reading request", err)
		return
	}

	log.Info(fmt.Sprintf("%s: Received query from %s", s.Conn().LocalPeer(), s.Conn().RemotePeer()))

	if filterRPCRequest.Request != nil {
		// We're on a full node.
		// This is a filter request coming from a light node.
		if filterRPCRequest.Request.Subscribe {
			subscriber := Subscriber{peer: string(s.Conn().RemotePeer()), requestId: filterRPCRequest.RequestId, filter: *filterRPCRequest.Request}
			wf.subscribers = append(wf.subscribers, subscriber)
			log.Info("Full node, add a filter subscriber ", subscriber)
		} else {
			// TODO wf.subscribers.unsubscribeFilters(filterRPCRequest.Request, conn.peerInfo.peerId)
		}
	} else if filterRPCRequest.Push != nil {
		// We're on a light node.
		// This is a message push coming from a full node.

		log.Info("Light node, received a message push ", *filterRPCRequest.Push)
		wf.pushHandler(filterRPCRequest.RequestId, *filterRPCRequest.Push)
	}

}

func NewWakuFilter(ctx context.Context, host host.Host, handler MessagePushHandler) *WakuFilter {
	wf := new(WakuFilter)
	wf.ctx = ctx
	wf.MsgC = make(chan *protocol.Envelope)
	wf.h = host
	wf.pushHandler = handler

	wf.h.SetStreamHandler(WakuFilterProtocolId, wf.onRequest)
	go wf.FilterListener()

	return wf
}

func (wf *WakuFilter) FilterListener() {

	// This function is invoked for each message received
	// on the full node in context of Waku2-Filter
	handle := func(envelope *protocol.Envelope) error { // async
		// trace "handle WakuFilter subscription", topic=topic, msg=msg

		msg := envelope.Message()
		topic := envelope.PubsubTopic()
		// Each subscriber is a light node that earlier on invoked
		// a FilterRequest on this node
		for _, subscriber := range wf.subscribers {
			if subscriber.filter.Topic != "" && subscriber.filter.Topic != topic {
				log.Info("Subscriber's filter pubsubTopic does not match message topic", subscriber.filter.Topic, topic)
				continue
			}

			for _, filter := range subscriber.filter.ContentFilters {
				if msg.ContentTopic == filter.ContentTopic {
					log.Info("Found matching contentTopic ", filter, msg)
					msgArr := []*pb.WakuMessage{msg}
					// Do a message push to light node
					pushRPC := &pb.FilterRPC{RequestId: subscriber.requestId, Push: &pb.MessagePush{Messages: msgArr}}
					log.Info("Pushing a message to light node: ", pushRPC)

					conn, err := wf.h.NewStream(wf.ctx, peer.ID(subscriber.peer), WakuFilterProtocolId)

					if err != nil {
						// @TODO more sophisticated error handling here
						log.Error("Failed to open peer stream")
						//waku_filter_errors.inc(labelValues = [dialFailure])
						return err
					}
					writer := protoio.NewDelimitedWriter(conn)
					err = writer.WriteMsg(pushRPC)
					if err != nil {
						log.Error("failed to push messages to remote peer")
						return nil
					}

				}
			}
		}

		return nil
	}

	for m := range wf.MsgC {
		handle(m)
	}

}

// TODO Remove code duplication
func (wf *WakuFilter) AddPeer(p peer.ID, addrs []ma.Multiaddr) error {
	for _, addr := range addrs {
		wf.h.Peerstore().AddAddr(p, addr, peerstore.PermanentAddrTTL)
	}
	err := wf.h.Peerstore().AddProtocols(p, string(WakuFilterProtocolId))
	if err != nil {
		return err
	}
	return nil
}

// Having a FilterRequest struct,
// select a peer with filter support, dial it,
// and submit FilterRequest wrapped in FilterRPC
func (wf *WakuFilter) Subscribe(ctx context.Context, request pb.FilterRequest) (string, error) { //.async, gcsafe.} {
	peer := wf.selectPeer()

	if peer != nil {
		conn, err := wf.h.NewStream(ctx, *peer, WakuFilterProtocolId)

		if conn != nil {
			// This is the only successful path to subscription
			id := protocol.GenerateRequestId()

			writer := protoio.NewDelimitedWriter(conn)
			filterRPC := &pb.FilterRPC{RequestId: hex.EncodeToString(id), Request: &request}
			log.Info("Sending filterRPC: ", filterRPC)
			err = writer.WriteMsg(filterRPC)
			return string(id), nil
		} else {
			// @TODO more sophisticated error handling here
			log.Error("failed to connect to remote peer")
			//waku_filter_errors.inc(labelValues = [dialFailure])
			return "", err
		}
	}

	return "", nil
}

func (wf *WakuFilter) Unsubscribe(ctx context.Context, request pb.FilterRequest) {
	// @TODO: NO REAL REASON TO GENERATE REQUEST ID FOR UNSUBSCRIBE OTHER THAN CREATING SANE-LOOKING RPC.
	peer := wf.selectPeer()

	if peer != nil {
		conn, err := wf.h.NewStream(ctx, *peer, WakuFilterProtocolId)

		if conn != nil {
			// This is the only successful path to subscription
			id := protocol.GenerateRequestId()

			writer := protoio.NewDelimitedWriter(conn)
			filterRPC := &pb.FilterRPC{RequestId: hex.EncodeToString(id), Request: &request}
			err = writer.WriteMsg(filterRPC)
			//return some(id)
		} else {
			// @TODO more sophisticated error handling here
			log.Error("failed to connect to remote peer", err)
			//waku_filter_errors.inc(labelValues = [dialFailure])
		}
	}
}