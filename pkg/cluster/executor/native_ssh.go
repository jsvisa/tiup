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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/pingcap/tiup/pkg/cliutil"
	"github.com/pingcap/tiup/pkg/cluster/ctxt"
	"github.com/pingcap/tiup/pkg/localdata"
	"github.com/pingcap/tiup/pkg/utils"
	"go.uber.org/zap"
)

var _ ctxt.Executor = &NativeSSHExecutor{}

func (e *NativeSSHExecutor) prompt(def string) string {
	if prom := os.Getenv(localdata.EnvNameSSHPassPrompt); prom != "" {
		return prom
	}
	return def
}

func (e *NativeSSHExecutor) configArgs(args []string) []string {
	if e.Config.Timeout != 0 {
		args = append(args, "-o", fmt.Sprintf("ConnectTimeout=%d", int64(e.Config.Timeout.Seconds())))
	}
	if e.Config.Password != "" {
		args = append([]string{"sshpass", "-p", e.Config.Password, "-P", e.prompt("password")}, args...)
	} else if e.Config.KeyFile != "" {
		args = append(args, "-i", e.Config.KeyFile)
		if e.Config.Passphrase != "" {
			args = append([]string{"sshpass", "-p", e.Config.Passphrase, "-P", e.prompt("passphrase")}, args...)
		}
	}
	return args
}

// Execute run the command via SSH, it's not invoking any specific shell by default.
func (e *NativeSSHExecutor) Execute(ctx context.Context, cmd string, sudo bool, timeout ...time.Duration) ([]byte, []byte, error) {
	if e.ConnectionTestResult != nil {
		return nil, nil, e.ConnectionTestResult
	}

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

	if len(timeout) > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout[0])
		defer cancel()
	}

	ssh := "ssh"

	if val := os.Getenv(localdata.EnvNameSSHPath); val != "" {
		if isExec := utils.IsExecBinary(val); !isExec {
			return nil, nil, fmt.Errorf("specified SSH in the environment variable `%s` does not exist or is not executable", localdata.EnvNameSSHPath)
		}
		ssh = val
	}

	args := []string{ssh, "-o", "StrictHostKeyChecking=no"}

	args = e.configArgs(args) // prefix and postfix args
	args = append(args, fmt.Sprintf("%s@%s", e.Config.User, e.Config.Host), cmd)

	command := exec.CommandContext(ctx, args[0], args[1:]...)

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()

	logfn := zap.L().Info
	if err != nil {
		logfn = zap.L().Error
	}
	logfn("SSHCommand",
		zap.String("host", e.Config.Host),
		zap.Int("port", e.Config.Port),
		zap.String("cmd", cmd),
		zap.Error(err),
		zap.String("stdout", stdout.String()),
		zap.String("stderr", stderr.String()))

	if err != nil {
		baseErr := ErrSSHExecuteFailed.
			Wrap(err, "Failed to execute command over SSH for '%s@%s:%d'", e.Config.User, e.Config.Host, e.Config.Port).
			WithProperty(ErrPropSSHCommand, cmd).
			WithProperty(ErrPropSSHStdout, stdout).
			WithProperty(ErrPropSSHStderr, stderr)
		if len(stdout.Bytes()) > 0 || len(stderr.Bytes()) > 0 {
			output := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
			baseErr = baseErr.
				WithProperty(cliutil.SuggestionFromFormat("Command output on remote host %s:\n%s\n",
					e.Config.Host,
					color.YellowString(output)))
		}
		return stdout.Bytes(), stderr.Bytes(), baseErr
	}

	return stdout.Bytes(), stderr.Bytes(), err
}

// Transfer copies files via SCP
// This function depends on `scp` (a tool from OpenSSH or other SSH implementation)
func (e *NativeSSHExecutor) Transfer(ctx context.Context, src, dst string, download bool, limit int) error {
	if e.ConnectionTestResult != nil {
		return e.ConnectionTestResult
	}

	scp := "scp"

	if val := os.Getenv(localdata.EnvNameSCPPath); val != "" {
		if isExec := utils.IsExecBinary(val); !isExec {
			return fmt.Errorf("specified SCP in the environment variable `%s` does not exist or is not executable", localdata.EnvNameSCPPath)
		}
		scp = val
	}

	args := []string{scp, "-r", "-o", "StrictHostKeyChecking=no"}
	if limit > 0 {
		args = append(args, "-l", fmt.Sprint(limit))
	}
	args = e.configArgs(args) // prefix and postfix args

	if download {
		targetPath := filepath.Dir(dst)
		if err := utils.CreateDir(targetPath); err != nil {
			return err
		}
		args = append(args, fmt.Sprintf("%s@%s:%s", e.Config.User, e.Config.Host, src), dst)
	} else {
		args = append(args, src, fmt.Sprintf("%s@%s:%s", e.Config.User, e.Config.Host, dst))
	}

	command := exec.Command(args[0], args[1:]...)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()

	logfn := zap.L().Info
	if err != nil {
		logfn = zap.L().Error
	}
	logfn("SCPCommand",
		zap.String("host", e.Config.Host),
		zap.Int("port", e.Config.Port),
		zap.String("cmd", strings.Join(args, " ")),
		zap.Error(err),
		zap.String("stdout", stdout.String()),
		zap.String("stderr", stderr.String()))

	if err != nil {
		baseErr := ErrSSHExecuteFailed.
			Wrap(err, "Failed to transfer file over SCP for '%s@%s:%d'", e.Config.User, e.Config.Host, e.Config.Port).
			WithProperty(ErrPropSSHCommand, strings.Join(args, " ")).
			WithProperty(ErrPropSSHStdout, stdout).
			WithProperty(ErrPropSSHStderr, stderr)
		if len(stdout.Bytes()) > 0 || len(stderr.Bytes()) > 0 {
			output := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
			baseErr = baseErr.
				WithProperty(cliutil.SuggestionFromFormat("Command output on remote host %s:\n%s\n",
					e.Config.Host,
					color.YellowString(output)))
		}
		return baseErr
	}

	return err
}
