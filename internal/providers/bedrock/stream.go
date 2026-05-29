package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/deepnoodle-ai/dive/llm"
)

var _ llm.StreamingLLM = (*Provider)(nil)

// Stream performs an InvokeModelWithResponseStream-style call and decodes the
// AWS event-stream framing into dive llm.Event values.
func (p *Provider) Stream(ctx context.Context, opts ...llm.Option) (llm.StreamIterator, error) {
	config := &llm.Config{}
	config.Apply(opts...)

	br, err := p.buildRequest(config, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(br)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal request: %w", err)
	}
	req, err := p.newHTTPRequest(ctx, "invoke-with-response-stream", body)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bedrock: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(raw)}
	}
	return &streamIterator{
		body:    resp.Body,
		decoder: eventstream.NewDecoder(),
	}, nil
}

// streamIterator decodes Bedrock's event-stream framing into llm.Event values.
type streamIterator struct {
	body    io.ReadCloser
	decoder *eventstream.Decoder
	current *llm.Event
	err     error
	done    bool
}

// chunkPayload is the JSON wrapper Bedrock puts in each event-stream frame.
type chunkPayload struct {
	Bytes []byte `json:"bytes"` // base64 in JSON; Go decodes []byte from base64 automatically
}

func (s *streamIterator) Next() bool {
	if s.done {
		return false
	}
	for {
		msg, err := s.decoder.Decode(s.body, nil)
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.done = true
				return false
			}
			s.err = fmt.Errorf("bedrock: decode event-stream frame: %w", err)
			s.done = true
			return false
		}

		// Surface gateway/Bedrock exception frames as errors.
		if mt := msg.Headers.Get(":message-type"); mt != nil && mt.String() == "exception" {
			et := ""
			if h := msg.Headers.Get(":exception-type"); h != nil {
				et = h.String()
			}
			s.err = fmt.Errorf("bedrock: stream exception %q: %s", et, string(msg.Payload))
			s.done = true
			return false
		}

		event, ok, err := decodeFrame(msg.Payload)
		if err != nil {
			s.err = err
			s.done = true
			return false
		}
		if !ok {
			continue // skip frames without a usable event (e.g. pings)
		}
		s.current = event
		return true
	}
}

// decodeFrame unwraps {"bytes": base64(anthropicEventJSON)} into an llm.Event.
// It returns ok=false for frames that do not carry an Anthropic event.
func decodeFrame(payload []byte) (*llm.Event, bool, error) {
	var wrapper chunkPayload
	inner := payload
	if err := json.Unmarshal(payload, &wrapper); err == nil && len(wrapper.Bytes) > 0 {
		inner = wrapper.Bytes
	}
	var event llm.Event
	if err := json.Unmarshal(inner, &event); err != nil {
		// Fall back: the inner bytes may be base64 not auto-decoded; try manual.
		decoded, derr := base64.StdEncoding.DecodeString(string(payload))
		if derr != nil {
			return nil, false, fmt.Errorf("bedrock: unmarshal stream event: %w", err)
		}
		if err := json.Unmarshal(decoded, &event); err != nil {
			return nil, false, fmt.Errorf("bedrock: unmarshal stream event: %w", err)
		}
	}
	if event.Type == "" {
		return nil, false, nil
	}
	return &event, true, nil
}

func (s *streamIterator) Event() *llm.Event { return s.current }

func (s *streamIterator) Err() error { return s.err }

func (s *streamIterator) Close() error { return s.body.Close() }
