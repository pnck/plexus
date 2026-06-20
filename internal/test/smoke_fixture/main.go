package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"plexus/internal/logger"
	"plexus/pkg/mesh"
	"plexus/protocol"
)

func main() {
	natsURL := flag.String("nats-url", "nats://127.0.0.1:4222", "NATS URL")
	agentID := flag.String("id", "agent-x", "Agent ID")
	groups := flag.String("groups", "", "Comma separated list of broadcast groups to join")
	queueGroups := flag.String("queue-groups", "", "Comma separated list of queue groups (group:queue format)")
	llmURL := flag.String("test-llm-url", "", "URL to hit for mock LLM requests")
	pingTargets := flag.String("test-ping-targets", "", "Comma separated list of agent IDs to ping continuously")
	debug := flag.Bool("debug", true, "Enable debug logs")
	flag.Parse()

	logger.Setup(*debug)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("Starting test-agent", "id", *agentID)
	var a *mesh.Node
	a = mesh.NewNode(*agentID, 
		mesh.WithNatsURL(*natsURL),
		mesh.WithOnMessage(func(msg protocol.Message) {
			reportMsg := protocol.Message{
				Sender:  *agentID,
				Target:  "server",
				Type:    protocol.TypeReport,
				Payload: []byte(fmt.Sprintf("RECEIVED:%s|FROM:%s", msg.Type.String(), msg.Sender)),
			}
			if a != nil {
				_ = a.SendRaw(context.Background(), "sys.report", reportMsg)
			}
		}),
	)
	if *groups != "" {
		for _, g := range strings.Split(*groups, ",") {
			if g = strings.TrimSpace(g); g != "" {
				_ = a.JoinGroup(ctx, g)
			}
		}
	}

	if *queueGroups != "" {
		for _, gq := range strings.Split(*queueGroups, ",") {
			parts := strings.Split(strings.TrimSpace(gq), ":")
			if len(parts) == 2 {
				_ = a.JoinQueueGroup(ctx, parts[0], parts[1])
			}
		}
	}

	// Run agent in background so we can do our test tasks
	go func() {
		if err := a.Run(ctx); err != nil {
			fmt.Printf("Agent run error: %v\n", err)
			os.Exit(1)
		}
	}()

	// Allow time for NATS connection to settle
	time.Sleep(500 * time.Millisecond)

	// Background Task 1: LLM API Pinger
	if *llmURL != "" {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					req, _ := http.NewRequestWithContext(ctx, "POST", *llmURL, strings.NewReader("PROMPT_FROM:"+*agentID))
					resp, err := http.DefaultClient.Do(req)
					if err == nil {
						body, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						reportMsg := protocol.Message{
							Sender:  *agentID,
							Target:  "server",
							Type:    protocol.TypeReport,
							Payload: []byte(fmt.Sprintf("SOURCE:LLM_API|MSG:%s", string(body))),
						}
						_ = a.SendRaw(ctx, "sys.report", reportMsg)
					}
					time.Sleep(500 * time.Millisecond)
				}
			}
		}()
	}

	// Background Task 2: P2P Cross Communicator
	if *pingTargets != "" {
		targets := strings.Split(*pingTargets, ",")
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					for _, target := range targets {
						target = strings.TrimSpace(target)
						if target == "" {
							continue
						}
						
						reportMsg := protocol.Message{
							Sender:  *agentID,
							Target:  "server",
							Type:    protocol.TypeReport,
							Payload: []byte(fmt.Sprintf("SOURCE:P2P[%s]|MSG:PING_FROM:%s", target, *agentID)),
						}
						_ = a.SendRaw(ctx, "sys.report", reportMsg)
						
						_ = a.SendMessage(ctx, target, []byte("PING_FROM:"+*agentID))
					}
					time.Sleep(1 * time.Second)
				}
			}
		}()
	}

	<-ctx.Done()
}
