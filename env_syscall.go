// +build !appengine

package kkonfig

import "syscall"

var lookupEnv = syscall.Getenv
