// Copyright 2020 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appleboy/easyssh-proxy"
	"github.com/fatih/color"
	"github.com/pingcap/tiup/pkg/cliutil"
	"github.com/pingcap/tiup/pkg/cluster/ctxt"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

var _ ctxt.Executor = &EasySSHExecutor{}

// Initialize builds and initializes a EasySSHExecutor
func (e *EasySSHExecutor) initialize(config SSHConfig) {
	// build easyssh config
	e.Config = &easyssh.MakeConfig{
		Server:  config.Host,
		Port:    strconv.Itoa(config.Port),
		User:    config.User,
		Timeout: config.Timeout, // timeout when connecting to remote
	}

	// prefer private key authentication
	if len(config.KeyFile) > 0 {
		e.Config.KeyPath = config.KeyFile
		e.Config.Passphrase = config.Passphrase
	} else if len(config.Password) > 0 {
		e.Config.Password = config.Password
	}
}

// Execute run the command via SSH, it's not invoking any specific shell by default.
func (e *EasySSHExecutor) Execute(ctx context.Context, cmd string, sudo bool, timeout ...time.Duration) ([]byte, []byte, error) {
	// try to acquire root permission
	if e.Sudo || sudo {
		cmd = fmt.Sprintf("sudo -H bash -c \"%s\"", cmd)
	}

	// set a basic PATH in case it's empty on login
	cmd = fmt.Sprintf("PATH=$PATH:/usr/bin:/usr/sbin %s", cmd)

	if e.Locale != "" {
		cmd = fmt.Sprintf("export LANG=%s; %s", e.Locale, cmd)
	}

	// run command on remote host
	// default timeout is 60s in easyssh-proxy
	if len(timeout) == 0 {
		timeout = append(timeout, executeDefaultTimeout)
	}

	stdout, stderr, done, err := e.Run(cmd, timeout...)

	logfn := zap.L().Info
	if err != nil {
		logfn = zap.L().Error
	}
	logfn("SSHCommand",
		zap.String("host", e.Config.Server),
		zap.String("port", e.Config.Port),
		zap.String("cmd", cmd),
		zap.Error(err),
		zap.String("stdout", stdout),
		zap.String("stderr", stderr))

	if err != nil {
		baseErr := ErrSSHExecuteFailed.
			Wrap(err, "Failed to execute command over SSH for '%s@%s:%s'", e.Config.User, e.Config.Server, e.Config.Port).
			WithProperty(ErrPropSSHCommand, cmd).
			WithProperty(ErrPropSSHStdout, stdout).
			WithProperty(ErrPropSSHStderr, stderr)
		if len(stdout) > 0 || len(stderr) > 0 {
			output := strings.TrimSpace(strings.Join([]string{stdout, stderr}, "\n"))
			baseErr = baseErr.
				WithProperty(cliutil.SuggestionFromFormat("Command output on remote host %s:\n%s\n",
					e.Config.Server,
					color.YellowString(output)))
		}
		return []byte(stdout), []byte(stderr), baseErr
	}

	if !done { // timeout case,
		return []byte(stdout), []byte(stderr), ErrSSHExecuteTimedout.
			Wrap(err, "Execute command over SSH timedout for '%s@%s:%s'", e.Config.User, e.Config.Server, e.Config.Port).
			WithProperty(ErrPropSSHCommand, cmd).
			WithProperty(ErrPropSSHStdout, stdout).
			WithProperty(ErrPropSSHStderr, stderr)
	}

	return []byte(stdout), []byte(stderr), nil
}

// Transfer copies files via SCP
// This function depends on `scp` (a tool from OpenSSH or other SSH implementation)
// This function is based on easyssh.MakeConfig.Scp() but with support of copying
// file from remote to local.
func (e *EasySSHExecutor) Transfer(ctx context.Context, src, dst string, download bool, limit int) error {
	// scp file from/to remote
	session, client, err := e.connect()
	if err != nil {
		return err
	}
	defer client.Close()
	defer session.Close()

	if download {
		return ScpDownload(session, client, src, dst, limit)
	}
	return ScpUpload(session, client, src, dst, limit)
}

// Run command on remote machine and returns its stdout as a string
func (e *EasySSHExecutor) Run(command string, timeout ...time.Duration) (outStr string, errStr string, isTimeout bool, err error) {
	stdoutChan, stderrChan, doneChan, errChan, err := e.Stream(command, timeout...)
	if err != nil {
		return outStr, errStr, isTimeout, err
	}
	// read from the output channel until the done signal is passed
loop:
	for {
		select {
		case isTimeout = <-doneChan:
			break loop
		case outline := <-stdoutChan:
			if outline != "" {
				outStr += outline + "\n"
			}
		case errline := <-stderrChan:
			if errline != "" {
				errStr += errline + "\n"
			}
		case err = <-errChan:
		}
	}
	// return the concatenation of all signals from the output channel
	return outStr, errStr, isTimeout, err
}

// Stream returns one channel that combines the stdout and stderr of the command
// as it is run on the remote machine, and another that sends true when the
// command is done. The sessions and channels will then be closed.
func (e *EasySSHExecutor) Stream(command string, timeout ...time.Duration) (<-chan string, <-chan string, <-chan bool, <-chan error, error) {
	// continuously send the command's output over the channel
	stdoutChan := make(chan string)
	stderrChan := make(chan string)
	doneChan := make(chan bool)
	errChan := make(chan error)

	// connect to remote host
	session, client, err := e.connect()
	if err != nil {
		return stdoutChan, stderrChan, doneChan, errChan, err
	}
	// defer session.Close()
	// connect to both outputs (they are of type io.Reader)
	outReader, err := session.StdoutPipe()
	if err != nil {
		client.Close()
		session.Close()
		return stdoutChan, stderrChan, doneChan, errChan, err
	}
	errReader, err := session.StderrPipe()
	if err != nil {
		client.Close()
		session.Close()
		return stdoutChan, stderrChan, doneChan, errChan, err
	}
	err = session.Start(command)
	if err != nil {
		client.Close()
		session.Close()
		return stdoutChan, stderrChan, doneChan, errChan, err
	}

	// combine outputs, create a line-by-line scanner
	stdoutReader := io.MultiReader(outReader)
	stderrReader := io.MultiReader(errReader)
	stdoutScanner := bufio.NewScanner(stdoutReader)
	stderrScanner := bufio.NewScanner(stderrReader)

	go func(stdoutScanner, stderrScanner *bufio.Scanner, stdoutChan, stderrChan chan string, doneChan chan bool, errChan chan error) {
		defer close(doneChan)
		defer close(errChan)
		defer client.Close()
		defer session.Close()

		// default timeout value
		executeTimeout := 60 * time.Second
		if len(timeout) > 0 {
			executeTimeout = timeout[0]
		}
		timeoutChan := time.After(executeTimeout)
		res := make(chan struct{}, 1)
		var resWg sync.WaitGroup
		resWg.Add(2)

		go func() {
			defer close(stdoutChan)
			for stdoutScanner.Scan() {
				stdoutChan <- stdoutScanner.Text()
			}
			resWg.Done()
		}()

		go func() {
			defer close(stderrChan)
			for stderrScanner.Scan() {
				stderrChan <- stderrScanner.Text()
			}
			resWg.Done()
		}()

		go func() {
			resWg.Wait()
			// close all of our open resources
			res <- struct{}{}
		}()

		select {
		case <-res:
			errChan <- session.Wait()
			doneChan <- true
		case <-timeoutChan:
			stderrChan <- "Run Command Timeout!"
			errChan <- nil
			doneChan <- false
		}
	}(stdoutScanner, stderrScanner, stdoutChan, stderrChan, doneChan, errChan)

	return stdoutChan, stderrChan, doneChan, errChan, err
}

func (e *EasySSHExecutor) connect() (*ssh.Session, *ssh.Client, error) {
	return e.Config.Connect()
}
