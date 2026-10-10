// Operator-only bounded mTLS/RPC acceptance check; no browser operations.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth"
	"github.com/ubag/ubag/apps/gateway/internal/helperclient"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
	"google.golang.org/grpc/status"
	"os"
	"time"
)

type registry struct{ entry nodes.RegistryEntry }

func (r registry) GetRegistry(context.Context, string) (nodes.RegistryEntry, error) {
	return r.entry, nil
}

func main() {
	endpoint := flag.String("endpoint", "", "private helper endpoint")
	node := flag.String("node", "", "expected node identity")
	pin := flag.String("pin", "", "accepted allocation SPKI SHA-256")
	directory := flag.String("tls-dir", "", "restricted primary certificate directory")
	version := flag.String("version", "", "pinned helper workload version")
	negative := flag.Bool("negative", false, "verify stale SPKI and wrong workload rejection")
	flag.Parse()
	if !nodes.ValidNodeID(*node) || len(*pin) != 64 || *endpoint == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "required endpoint/node/pin/version")
		os.Exit(2)
	}
	ca, err := helperauth.LoadCAPool(*directory + "/ca.pem")
	if err != nil {
		panic("primary CA unreadable")
	}
	kp, err := helperauth.NewKeyPair(*directory+"/primary.crt", *directory+"/primary.key")
	if err != nil {
		panic("primary key pair unreadable")
	}
	dial := func(p, v string) (*helperv1.HandshakeResponse, error) {
		d, err := helperclient.NewFromKeyPair(ca, kp, registry{nodes.RegistryEntry{NodeID: *node, URISAN: nodes.NodeURISAN(*node), SPKICurrent: p}})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		c, err := d.Dial(ctx, *node, *endpoint)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		return c.Handshake(ctx, &helperv1.HandshakeRequest{ProtocolVersion: "ubag.helper.v1", PrimaryWorkloadVersion: v, NodeId: *node})
	}
	handshake, err := dial(*pin, *version)
	if err != nil {
		fmt.Println("handshake failed:", status.Code(err))
		os.Exit(1)
	}
	d, _ := helperclient.NewFromKeyPair(ca, kp, registry{nodes.RegistryEntry{NodeID: *node, URISAN: nodes.NodeURISAN(*node), SPKICurrent: *pin}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := d.Dial(ctx, *node, *endpoint)
	if err != nil {
		panic("capacity dial failed")
	}
	defer c.Close()
	capacity, err := c.ReportCapacity(ctx, &helperv1.ReportCapacityRequest{NodeId: *node})
	if err != nil {
		fmt.Println("capacity failed:", status.Code(err))
		os.Exit(1)
	}
	result := map[string]any{"handshake": handshake, "capacity": capacity}
	if *negative {
		bad := "0000000000000000000000000000000000000000000000000000000000000000"
		_, pinErr := dial(bad, *version)
		wrongVersion := "sha-0000000000000000000000000000000000000000"
		wrongHandshake, versionErr := dial(*pin, wrongVersion)
		// Handshake advertises the version; the primary rejects mismatches before execution.
		versionMismatch := versionErr != nil || (wrongHandshake != nil && wrongHandshake.GetWorkloadVersion() != wrongVersion)
		result["stale_pin_rejected"], result["wrong_workload_detected"] = pinErr != nil, versionMismatch
		if pinErr == nil || !versionMismatch {
			fmt.Println("negative acceptance failed")
			os.Exit(1)
		}
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
