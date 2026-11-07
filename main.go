package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("mcp-mfjs-proxy", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	noRename := flags.Bool("no-rename", false, "leave tool names unchanged")
	report := flags.Bool("report", false, "report rewrites to stderr")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: mcp-mfjs-proxy [--no-rename] [--report] -- <upstream command> [args...]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	command := flags.Args()
	if len(command) == 0 {
		flags.Usage()
		return 2
	}

	logger := log.New(os.Stderr, "mcp-mfjs-proxy: ", 0)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = os.Stderr

	upstreamStdin, err := cmd.StdinPipe()
	if err != nil {
		logger.Printf("create upstream stdin: %v", err)
		return 1
	}
	upstreamStdout, err := cmd.StdoutPipe()
	if err != nil {
		logger.Printf("create upstream stdout: %v", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		logger.Printf("start upstream: %v", err)
		return 1
	}

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(signals)
	go func() {
		for sig := range signals {
			if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
				logger.Printf("forward signal %s: %v", sig, err)
			}
		}
	}()

	rewriter := newMessageRewriter(*noRename, *report, logger)
	go func() {
		if err := pump(os.Stdin, upstreamStdin, rewriter.rewriteClientMessage); err != nil {
			logger.Printf("relay client input: %v", err)
		}
		if err := upstreamStdin.Close(); err != nil {
			logger.Printf("close upstream stdin: %v", err)
		}
	}()

	outputErr := pump(upstreamStdout, os.Stdout, rewriter.rewriteUpstreamMessage)
	if outputErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if outputErr != nil {
		logger.Printf("relay upstream output: %v", outputErr)
		return 1
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return 128 + int(status.Signal())
			}
			return exitErr.ExitCode()
		}
		logger.Printf("wait for upstream: %v", waitErr)
		return 1
	}
	return 0
}

func pump(src io.Reader, dst io.Writer, rewrite func([]byte) []byte) error {
	reader := bufio.NewReader(src)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := dst.Write(rewrite(line)); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
