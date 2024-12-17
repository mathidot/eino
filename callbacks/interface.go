/*
 * Copyright 2024 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package callbacks

import (
	"context"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/schema"
)

// RunInfo is the info of run node.
type RunInfo struct {
	Name      string
	Type      string
	Component components.Component
}

// CallbackInput is the input of the callback.
// the type of input is defined by the component.
// using type Assert or convert func to convert the input to the right type you want.
// e.g.
//
//		CallbackInput in components/model/interface.go is:
//		type CallbackInput struct {
//			Messages []*schema.Message
//			Config   *Config
//			Extra map[string]any
//		}
//
//	 and provide a func of model.ConvCallbackInput() to convert CallbackInput to *model.CallbackInput
//	 in callback handler, you can use the following code to get the input:
//
//		modelCallbackInput := model.ConvCallbackInput(in)
//		if modelCallbackInput == nil {
//			// is not a model callback input, just ignore it
//			return
//		}
type CallbackInput any

type CallbackOutput any

type Handler interface {
	OnStart(ctx context.Context, info *RunInfo, input CallbackInput) context.Context
	OnEnd(ctx context.Context, info *RunInfo, output CallbackOutput) context.Context

	OnError(ctx context.Context, info *RunInfo, err error) context.Context

	OnStartWithStreamInput(ctx context.Context, info *RunInfo,
		input *schema.StreamReader[CallbackInput]) context.Context
	OnEndWithStreamOutput(ctx context.Context, info *RunInfo,
		output *schema.StreamReader[CallbackOutput]) context.Context
}

var globalHandlers []Handler

// InitCallbackHandlers sets the global callback handlers.
// It should be called BEFORE any callback handler by user.
// It's useful when you want to inject some basic callbacks to all nodes.
func InitCallbackHandlers(handlers []Handler) {
	globalHandlers = handlers
}

func GetGlobalHandlers() []Handler {
	return globalHandlers
}

// CallbackTiming enumerates all the timing of callback aspects.
type CallbackTiming uint8

const (
	TimingOnStart CallbackTiming = iota
	TimingOnEnd
	TimingOnError
	TimingOnStartWithStreamInput
	TimingOnEndWithStreamOutput
)

// TimingChecker checks if the handler is needed for the given callback aspect timing.
// It's recommended for callback handlers to implement this interface, but not mandatory.
// If a callback handler is created by using callbacks.HandlerHelper or handlerBuilder, then this interface is automatically implemented.
// Eino's callback mechanism will try to use this interface to determine whether any handlers are needed for the given timing.
// Also, the callback handler that is not needed for that timing will be skipped.
type TimingChecker interface {
	Needed(ctx context.Context, info *RunInfo, timing CallbackTiming) bool
}
