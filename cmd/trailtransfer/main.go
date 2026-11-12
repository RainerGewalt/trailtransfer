// Command trailtransfer is an MQTT-controlled, policy-guarded rclone worker for
// edge file transfers that seals every outcome in a verifiable journal.
//
// Usage:
//
//	trailtransfer run [--config path]
//	trailtransfer validate-config [--config path]
//	trailtransfer print-capabilities [--config path]
//	trailtransfer version
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RainerGewalt/trailtransfer/internal/config"
	"github.com/RainerGewalt/trailtransfer/internal/events"
	"github.com/RainerGewalt/trailtransfer/internal/evidence"
	"github.com/RainerGewalt/trailtransfer/internal/health"
	"github.com/RainerGewalt/trailtransfer/internal/jobs"
	"github.com/RainerGewalt/trailtransfer/internal/logging"
	"github.com/RainerGewalt/trailtransfer/internal/mqtt"
	"github.com/RainerGewalt/trailtransfer/internal/policy"
	"github.com/RainerGewalt/trailtransfer/internal/rclone"
	"github.com/RainerGewalt/trailtransfer/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "validate-config":
		cmdValidateConfig(os.Args[2:])
	case "print-capabilities":
		cmdPrintCapabilities(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "keygen":
		cmdKeygen(os.Args[2:])
	case "version":
		fmt.Println(version.String())
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `trailtransfer — MQTT-controlled rclone worker for edge file transfers with a verifiable journal

Commands:
  run                 start the worker
  validate-config     load config + policy, report validity, exit 0/1
  print-capabilities  print the capabilities event as JSON
  verify <journal>    independently verify an evidence journal, exit 0/1
                      (--pubkey key.pub: every record must carry a valid signature)
  keygen [--out name] generate an Ed25519 signing key pair (<name>.key/.pub)
  version             print the worker version

Flags (run/validate-config/print-capabilities):
  --config <path>     path to a YAML config file (optional; TRAILTRANSFER_* env overrides)
`)
}

func cmdRun(args []string) {
	cfgPath := parseConfigFlag("run", args)
	cfg, pol, err := loadAll(cfgPath)
	if err != nil {
		fatal(err)
	}
	log := logging.Setup(cfg.LogLevel)
	log.Info("starting TrailTransfer", "worker_id", cfg.WorkerID, "version", version.String(), "policy_version", pol.PolicyVersion)

	runner := &rclone.Runner{
		RclonePath:   cfg.RcloneBinary,
		RcloneConfig: cfg.RcloneConfig,
		MaxSizeMB:    pol.MaxFileSizeMB,
	}
	if !rclone.Available(cfg.RcloneBinary) {
		log.Warn("rclone binary not found — transfer jobs will fail until it is on PATH", "binary", cfg.RcloneBinary)
	}

	sealOpts := evidence.Options{
		ProducerID:      cfg.WorkerID,
		ProducerVersion: version.String(),
		Policy:          evidencePolicy(cfg, pol),
		JournalPath:     cfg.EvidenceJournal,
	}
	if cfg.SigningKey != "" {
		signer, err := evidence.LoadSigner(cfg.SigningKey)
		if err != nil {
			fatal(fmt.Errorf("signing key: %w", err))
		}
		sealOpts.Signer = signer
		log.Info("evidence signing enabled", "key_id", signer.ID())
	}
	sealer, err := evidence.NewSealer(sealOpts)
	if err != nil {
		fatal(fmt.Errorf("evidence journal: %w", err))
	}
	defer sealer.Close()
	if sealer.JournalEnabled() {
		log.Info("evidence journal enabled", "path", cfg.EvidenceJournal, "schema", evidence.Schema)
	} else {
		log.Warn("evidence journal disabled — hash chain is in-memory only; set TRAILTRANSFER_EVIDENCE_JOURNAL for durable, recoverable evidence")
	}

	lwt, _ := json.Marshal(events.HealthEvent{
		EventType: events.TypeHealth,
		Status:    "offline",
		WorkerID:  cfg.WorkerID,
		Version:   version.String(),
	})

	client := mqtt.New(mqtt.Options{
		Broker:     cfg.BrokerURL(),
		ClientID:   "trailtransfer-" + cfg.WorkerID,
		Username:   cfg.MQTT.Username,
		Password:   cfg.MQTT.Password,
		KeepAlive:  time.Duration(cfg.MQTT.KeepAliveSeconds) * time.Second,
		LWTTopic:   cfg.HealthTopic(),
		LWTPayload: lwt,
	})

	pub := events.NewPublisher(client, cfg, version.String(), sealer)
	mgr := jobs.NewManager(pol, pub, runner, log)

	caps := events.CapabilitiesEvent{
		SupportedActions: mgr.SupportedActions(),
		MaxParallelJobs:  pol.MaxParallelJobs,
		RcloneAvailable:  rclone.Available(cfg.RcloneBinary),
		EvidenceSchema:   evidence.Schema,
		EvidenceJournal:  sealer.JournalEnabled(),
		EvidenceSigned:   sealer.Signed(),
		PolicyVersion:    pol.PolicyVersion,
		PolicyHash:       pol.Hash,
	}

	client.OnConnect(func(c *mqtt.Client) {
		if err := c.Subscribe(cfg.CommandTopic(), 1, func(_ string, payload []byte) {
			mgr.Handle(payload)
		}); err != nil {
			log.Error("subscribe failed", "topic", cfg.CommandTopic(), "error", err)
			return
		}
		log.Info("subscribed to command topic", "topic", cfg.CommandTopic())
		pub.Capabilities(caps)
	})

	if err := client.Connect(); err != nil {
		fatal(fmt.Errorf("mqtt connect: %w", err))
	}
	log.Info("connected to broker", "broker", cfg.BrokerURL())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	health.StartLoop(ctx, pub, pol, mgr, cfg.RcloneBinary, time.Duration(cfg.HealthIntervalSeconds)*time.Second)

	<-ctx.Done()
	log.Info("shutting down")
	pub.Health(events.HealthEvent{
		Status:           "stopped",
		MaxParallelJobs:  pol.MaxParallelJobs,
		PolicyVersion:    pol.PolicyVersion,
		SupportedActions: mgr.SupportedActions(),
	})
	client.Disconnect()
}

func cmdValidateConfig(args []string) {
	cfgPath := parseConfigFlag("validate-config", args)
	cfg, pol, err := loadAll(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid:", err)
		os.Exit(1)
	}
	fmt.Printf("ok: worker_id=%s policy_version=%s allowed_actions=%v max_parallel_jobs=%d\n",
		cfg.WorkerID, pol.PolicyVersion, pol.AllowedActions, pol.MaxParallelJobs)
}

func cmdPrintCapabilities(args []string) {
	cfgPath := parseConfigFlag("print-capabilities", args)
	cfg, pol, err := loadAll(cfgPath)
	if err != nil {
		fatal(err)
	}
	caps := events.CapabilitiesEvent{
		EventType:        events.TypeCapabilities,
		WorkerID:         cfg.WorkerID,
		Version:          version.String(),
		SupportedActions: pol.SupportedActions(),
		MaxParallelJobs:  pol.MaxParallelJobs,
		RcloneAvailable:  rclone.Available(cfg.RcloneBinary),
		EvidenceSchema:   evidence.Schema,
		EvidenceJournal:  cfg.EvidenceJournal != "",
		EvidenceSigned:   cfg.SigningKey != "",
		PolicyVersion:    pol.PolicyVersion,
		PolicyHash:       pol.Hash,
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.MarshalIndent(caps, "", "  ")
	fmt.Println(string(b))
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	journal := fs.String("journal", "", "path to the evidence journal (JSONL)")
	pubkey := fs.String("pubkey", "", "PEM Ed25519 public key to verify record signatures")
	_ = fs.Parse(args)
	path := *journal
	if path == "" && fs.NArg() > 0 {
		path = fs.Arg(0)
		// Accept flags after the path too ("verify j.jsonl --pubkey k.pub"):
		// Go's flag package stops at the first positional argument, which
		// used to drop --pubkey silently and report OK without checking
		// signatures.
		_ = fs.Parse(fs.Args()[1:])
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "unexpected argument %q\n", fs.Arg(0))
			fmt.Fprintln(os.Stderr, "usage: trailtransfer verify [--pubkey key.pub] <journal-path>")
			os.Exit(2)
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "usage: trailtransfer verify [--pubkey key.pub] <journal-path>")
		os.Exit(2)
	}

	var pub ed25519.PublicKey
	if *pubkey != "" {
		p, err := evidence.LoadPublicKey(*pubkey)
		if err != nil {
			fatal(err)
		}
		pub = p
	}

	rep, err := evidence.Verify(path, pub)
	if err != nil {
		fatal(err)
	}
	if rep.Entries == 0 {
		// Nothing was checked, which is not the same as nothing being wrong.
		fmt.Println("EMPTY: 0 record(s), nothing was verified; check that this is the journal you meant")
		os.Exit(1)
	}
	verdict := "OK"
	if !rep.OK {
		verdict = "FAIL"
	}
	fmt.Printf("%s: %d record(s), %d restart(s)", verdict, rep.Entries, rep.Restarts)
	switch {
	case pub != nil:
		fmt.Printf(", %d signature(s) verified", rep.SignaturesChecked)
		if rep.Unsigned > 0 {
			fmt.Printf(", %d unsigned record(s)", rep.Unsigned)
		}
	case rep.Signed:
		fmt.Print(", records are signed (pass --pubkey to verify signatures)")
	}
	fmt.Println()
	for _, code := range []string{
		evidence.CodeSchemaInvalid, evidence.CodeHashMismatch, evidence.CodeChainBreak,
		evidence.CodeSeqGap, evidence.CodeSignatureInvalid, evidence.CodeRestart,
	} {
		if n := rep.Count(code); n > 0 {
			fmt.Printf("  %-18s %d\n", code, n)
		}
	}
	for _, f := range rep.Findings {
		if f.Code == evidence.CodeRestart && rep.OK {
			// Restarts are informational; list them only next to real
			// findings, except the one that says records are missing.
			if f.Index == 0 && rep.PreviousSegmentMissing {
				fmt.Println("  note: " + f.String())
			}
			continue
		}
		fmt.Println("  - " + f.String())
	}
	printNotChecked(rep, pub != nil)
	if !rep.OK {
		os.Exit(1)
	}
}

// printNotChecked says what a verify run does not establish, as plainly as
// the verdict says what it does (docs/EVIDENCE_MODEL.md, threat table).
func printNotChecked(rep evidence.VerifyReport, withKey bool) {
	fmt.Println("not checked:")
	switch {
	case withKey:
	case rep.Signed:
		fmt.Println("  - signatures: no --pubkey given")
	default:
		fmt.Println("  - signatures: the records are not signed")
	}
	fmt.Println("  - records cut off after the last record, or a journal replaced as a whole: compare the last chain_hash with a copy kept outside the journal")
	fmt.Println("  - the transferred files themselves: the journal holds no file manifest")
}

// evidencePolicy maps the loaded worker policy to the record's policy member.
func evidencePolicy(cfg *config.Config, pol *policy.Policy) evidence.Policy {
	return evidence.Policy{
		ID:      cfg.PolicyID,
		Version: pol.PolicyVersion,
		Hash:    pol.Hash,
		Kind:    evidence.PolicyKindTransfer,
	}
}

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "trailtransfer", "output basename (writes <out>.key and <out>.pub)")
	_ = fs.Parse(args)

	priv, pub, keyID, err := evidence.GenerateKeyPair()
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out+".key", priv, 0o600); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out+".pub", pub, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s.key (private, keep secret) and %s.pub\nkey_id: %s\n", *out, *out, keyID)
}

func parseConfigFlag(name string, args []string) string {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("TRAILTRANSFER_CONFIG"), "path to a YAML config file (optional)")
	_ = fs.Parse(args)
	return *cfgPath
}

func loadAll(cfgPath string) (*config.Config, *policy.Policy, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	pol, err := policy.Load(cfg.PolicyFile)
	if err != nil {
		return nil, nil, err
	}
	return cfg, pol, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
