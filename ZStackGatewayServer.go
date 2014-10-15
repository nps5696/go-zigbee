package zigbee

import (
	"fmt"
	"time"

	"code.google.com/p/gogoprotobuf/proto"
	"github.com/davecgh/go-spew/spew"
	"github.com/ninjasphere/go-zigbee/gateway"
)

type ZStackGateway struct {
	*ZStackServer
	pendingResponses map[uint32]*pendingGatewayResponse
}

type zStackGatewayCommand interface {
	proto.Message
	GetCmdId() gateway.GwCmdIdT
}

type pendingGatewayResponse struct {
	response zStackGatewayCommand
	finished chan error
}

func (s *ZStackGateway) waitForSequenceResponse(sequenceNumber uint32, response zStackGatewayCommand, timeoutDuration time.Duration) error {
	// We accept uint32 as thats what comes back from protobuf
	log.Debugf("Waiting for sequence %d", sequenceNumber)
	_, exists := s.pendingResponses[sequenceNumber]
	if exists {
		s.pendingResponses[sequenceNumber].finished <- fmt.Errorf("Another command with the same sequence id (%d) has been sent.", sequenceNumber)
	}

	pending := &pendingGatewayResponse{
		response: response,
		finished: make(chan error),
	}
	s.pendingResponses[sequenceNumber] = pending

	timeout := make(chan bool, 1)
	go func() {
		time.Sleep(timeoutDuration)
		timeout <- true
	}()

	var err error

	select {
	case error := <-pending.finished:
		err = error
	case <-timeout:
		err = fmt.Errorf("The request timed out after %s", timeoutDuration)
	}

	s.pendingResponses[sequenceNumber] = nil

	return err
}

// SendAsyncCommand sends a command that requires an async response from the device, using ZCL SequenceNumber
func (s *ZStackGateway) SendAsyncCommand(request zStackGatewayCommand, response zStackGatewayCommand, timeout time.Duration) error {
	confirmation := &gateway.GwZigbeeGenericCnf{}

	//	spew.Dump("sending", request)

	err := s.SendCommand(request, confirmation)

	if err != nil {
		return err
	}

	//spew.Dump(confirmation)

	if confirmation.Status.String() != "STATUS_SUCCESS" {
		return fmt.Errorf("Invalid confirmation status: %s", confirmation.Status.String())
	}

	return s.waitForSequenceResponse(*confirmation.SequenceNumber, response, timeout)
}

// SendCommand sends a protobuf Message to the Z-Stack server, and waits for the response
func (s *ZStackGateway) SendCommand(request zStackGatewayCommand, response zStackGatewayCommand) error {

	return s.sendCommand(
		&zStackCommand{
			message:   request,
			commandID: uint8(request.GetCmdId()),
		},
		&zStackCommand{
			message:   response,
			commandID: uint8(response.GetCmdId()),
		},
	)

}

func (s *ZStackGateway) onIncomingCommand(commandID uint8, bytes *[]byte) {

	//bytes := <-s.Incoming

	log.Debugf("gateway: Got gateway message % X", bytes)

	//commandID := uint8((*bytes)[1])

	if commandID == uint8(gateway.GwCmdIdT_GW_ATTRIBUTE_REPORTING_IND) {
		report := &gateway.GwAttributeReportingInd{}
		err := proto.Unmarshal(*bytes, report)
		if err != nil {
			log.Errorf("gateway: Could not read attribute report: %s, %v", err, *bytes)
			return
		}

		if log.IsDebugEnabled() {
			spew.Dump("Got attribute report", report)
		}

		return
	}

	var message = &gateway.GwZigbeeGenericRspInd{} // Not always this, but it will always give us the sequence number?
	err := proto.Unmarshal(*bytes, message)
	if err != nil {
		log.Errorf("gateway: Could not get sequence number from incoming gateway message : %s, %v", err, *bytes)
		return
	}

	sequenceNumber := *message.SequenceNumber

	log.Debugf("gateway: Got an incoming gateway message, sequence:%d", sequenceNumber)

	if sequenceNumber == 0 {
		log.Debugf("gateway: Failed to get a sequence number from an incoming gateway message ????")
	}

	pending := s.pendingResponses[sequenceNumber]

	if pending == nil {
		log.Debugf("gateway: Received response to sequence number %d but we aren't listening for it", sequenceNumber)
	} else {

		if uint8(pending.response.GetCmdId()) != commandID {
			pending.finished <- fmt.Errorf("Wrong ZCL response type. Wanted: 0x%X Received: 0x%X", uint8(pending.response.GetCmdId()), commandID)
		}
		pending.finished <- proto.Unmarshal(*bytes, pending.response)
	}

}

func ConnectToGatewayServer(hostname string, port int) (*ZStackGateway, error) {
	server, err := connectToServer("Gateway", uint8(gateway.ZStackGwSysIdT_RPC_SYS_PB_GW), hostname, port)
	if err != nil {
		return nil, err
	}

	gateway := &ZStackGateway{
		ZStackServer:     server,
		pendingResponses: make(map[uint32]*pendingGatewayResponse),
	}

	server.onIncoming = func(commandID uint8, bytes *[]byte) {
		gateway.onIncomingCommand(commandID, bytes)
	}

	return gateway, nil
}