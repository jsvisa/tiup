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
	"fmt"
	"os"
	"time"

	"github.com/appleboy/easyssh-proxy"
	"github.com/joomcode/errorx"
)

var (
	errNSSSH = errNS.NewSubNamespace("ssh")

	// ErrPropSSHCommand is ErrPropSSHCommand
	ErrPropSSHCommand = errorx.RegisterPrintableProperty("ssh_command")
	// ErrPropSSHStdout is ErrPropSSHStdout
	ErrPropSSHStdout = errorx.RegisterPrintableProperty("ssh_stdout")
	// ErrPropSSHStderr is ErrPropSSHStderr
	ErrPropSSHStderr = errorx.RegisterPrintableProperty("ssh_stderr")

	// ErrSSHExecuteFailed is ErrSSHExecuteFailed
	ErrSSHExecuteFailed = errNSSSH.NewType("execute_failed")
	// ErrSSHExecuteTimedout is ErrSSHExecuteTimedout
	ErrSSHExecuteTimedout = errNSSSH.NewType("execute_timedout")
)

func init() {
	v := os.Getenv("TIUP_CLUSTER_EXECUTE_DEFAULT_TIMEOUT")
	if v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			fmt.Println("ignore invalid TIUP_CLUSTER_EXECUTE_DEFAULT_TIMEOUT: ", v)
			return
		}

		executeDefaultTimeout = d
	}
}

type (
	// EasySSHExecutor implements Executor with EasySSH as transportation layer.
	EasySSHExecutor struct {
		Config *easyssh.MakeConfig
		Locale string // the locale used when executing the command
		Sudo   bool   // all commands run with this executor will be using sudo
	}

	// NativeSSHExecutor implements Excutor with native SSH transportation layer.
	NativeSSHExecutor struct {
		Config               *SSHConfig
		Locale               string // the locale used when executing the command
		Sudo                 bool   // all commands run with this executor will be using sudo
		ConnectionTestResult error  // test if the connection can be established in initialization phase
	}

	// SSHConfig is the configuration needed to establish SSH connection.
	SSHConfig struct {
		Host       string // hostname of the SSH server
		Port       int    // port of the SSH server
		User       string // username to login to the SSH server
		Password   string // password of the user
		KeyFile    string // path to the private key file
		Passphrase string // passphrase of the private key file
		// Timeout is the maximum amount of time for the TCP connection to establish.
		Timeout time.Duration
	}
)
