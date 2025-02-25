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

package compose

import (
	"context"
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

// Runnable is the interface for an executable object. Graph, Chain can be compiled into Runnable.
// runnable is the core conception of eino, we do downgrade compatibility for four data flow patterns,
// and can automatically connect components that only implement one or more methods.
// eg, if a component only implements Stream() method, you can still call Invoke() to convert stream output to invoke output.
type Runnable[I, O any] interface {
	Invoke(ctx context.Context, input I, opts ...Option) (output O, err error)
	Stream(ctx context.Context, input I, opts ...Option) (output *schema.StreamReader[O], err error)
	Collect(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output O, err error)
	Transform(ctx context.Context, input *schema.StreamReader[I], opts ...Option) (output *schema.StreamReader[O], err error)
}

type invoke func(ctx context.Context, input any, opts ...any) (output any, err error)
type transform func(ctx context.Context, input streamReader, opts ...any) (output streamReader, err error)

type streamMapFilter func(key string, isr streamReader) (streamReader, bool)

type valueHandler func(value any) (any, error)
type streamHandler func(streamReader) streamReader

type handlerPair struct {
	invoke    valueHandler
	transform streamHandler
}

// composableRunnable the wrapper for all executable object directly provided by the user.
// one instance corresponds to one instance of the executable object.
// all information comes from executable object without any other dimensions of information.
// for the graphNode, ChainBranch, StatePreHandler, StatePostHandler etc.
type composableRunnable struct {
	i invoke
	t transform

	// when set input key, use this method to convert input from map[string]any to T
	inputStreamFilter streamMapFilter
	// when predecessor's output is assignableTypeMay to current node's input, validate and convert(if needed) types using the following two methods
	inputConverter handlerPair
	// when current node enable field mapping, convert map input to expected struct using the following two methods
	inputFieldMappingConverter handlerPair

	inputType  reflect.Type
	outputType reflect.Type
	optionType reflect.Type

	isPassthrough bool

	meta *executorMeta

	// only available when in Graph node
	// if composableRunnable not in Graph node, this field would be nil
	nodeInfo *nodeInfo
}

// nolint: byted_s_args_length_limit
func runnableLambda[I, O, TOption any](i Invoke[I, O, TOption], s Stream[I, O, TOption], c Collect[I, O, TOption],
	t Transform[I, O, TOption], enableCallback bool) *composableRunnable {
	rp := newRunnablePacker(i, s, c, t, enableCallback)

	return rp.toComposableRunnable()
}

type runnablePacker[I, O, TOption any] struct {
	i Invoke[I, O, TOption]
	s Stream[I, O, TOption]
	c Collect[I, O, TOption]
	t Transform[I, O, TOption]
}

func (rp *runnablePacker[I, O, TOption]) wrapRunnableCtx(ctxWrapper func(ctx context.Context, opts ...TOption) context.Context) {
	i, s, c, t := rp.i, rp.s, rp.c, rp.t
	rp.i = func(ctx context.Context, input I, opts ...TOption) (output O, err error) {
		ctx = ctxWrapper(ctx, opts...)
		return i(ctx, input, opts...)
	}
	rp.s = func(ctx context.Context, input I, opts ...TOption) (output *schema.StreamReader[O], err error) {
		ctx = ctxWrapper(ctx, opts...)
		return s(ctx, input, opts...)
	}
	rp.c = func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output O, err error) {
		ctx = ctxWrapper(ctx, opts...)
		return c(ctx, input, opts...)
	}

	rp.t = func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output *schema.StreamReader[O], err error) {
		ctx = ctxWrapper(ctx, opts...)
		return t(ctx, input, opts...)
	}
}

func defaultStreamMapFilter[T any](key string, isr streamReader) (streamReader, bool) {
	sr, ok := unpackStreamReader[map[string]any](isr)
	if !ok {
		return nil, false
	}

	convert := func(m map[string]any) (T, error) {
		var t T
		v, ok_ := m[key]
		if !ok_ {
			return t, schema.ErrNoValue
		}
		vv, ok_ := v.(T)
		if !ok_ {
			return t, fmt.Errorf(
				"[defaultStreamMapFilter]fail, key[%s]'s value type[%s] isn't expected type[%s]",
				key, reflect.TypeOf(v).String(),
				generic.TypeOf[T]().String())
		}
		return vv, nil
	}

	ret := schema.StreamReaderWithConvert[map[string]any, T](sr, convert)

	return packStreamReader(ret), true
}

func defaultStreamConverter[T any](reader streamReader) streamReader {
	return packStreamReader(schema.StreamReaderWithConvert(reader.toAnyStreamReader(), func(v any) (T, error) {
		vv, ok := v.(T)
		if !ok {
			var t T
			return t, fmt.Errorf("runtime type check fail, expected type: %T, actual type: %T", t, v)
		}
		return vv, nil
	}))
}

func defaultValueChecker[T any](v any) (any, error) {
	nValue, ok := v.(T)
	if !ok {
		var t T
		return nil, fmt.Errorf("runtime type check fail, expected type: %T, actual type: %T", t, v)
	}
	return nValue, nil
}

func (rp *runnablePacker[I, O, TOption]) toComposableRunnable() *composableRunnable {
	inputType := generic.TypeOf[I]()
	outputType := generic.TypeOf[O]()
	optionType := generic.TypeOf[TOption]()
	c := &composableRunnable{
		inputStreamFilter: defaultStreamMapFilter[I],
		inputConverter: handlerPair{
			invoke:    defaultValueChecker[I],
			transform: defaultStreamConverter[I],
		},
		inputFieldMappingConverter: handlerPair{
			invoke:    buildFieldMappingConverter[I](),
			transform: buildStreamFieldMappingConverter[I](),
		},
		inputType:  inputType,
		outputType: outputType,
		optionType: optionType,
	}

	i := func(ctx context.Context, input any, opts ...any) (output any, err error) {
		in, ok := input.(I)
		if !ok {
			panic(newUnexpectedInputTypeErr(inputType, reflect.TypeOf(input)))
		}

		tos, err := convertOption[TOption](opts...)
		if err != nil {
			return nil, err
		}
		return rp.Invoke(ctx, in, tos...)
	}

	t := func(ctx context.Context, input streamReader, opts ...any) (output streamReader, err error) {
		in, ok := unpackStreamReader[I](input)
		if !ok {
			panic(newUnexpectedInputTypeErr(reflect.TypeOf(in), input.getType()))
		}

		tos, err := convertOption[TOption](opts...)
		if err != nil {
			return nil, err
		}

		out, err := rp.Transform(ctx, in, tos...)
		if err != nil {
			return nil, err
		}

		return packStreamReader(out), nil
	}

	c.i = i
	c.t = t

	return c
}

// Invoke works like `ping => pong`.
func (rp *runnablePacker[I, O, TOption]) Invoke(ctx context.Context,
	input I, opts ...TOption) (output O, err error) {
	return rp.i(ctx, input, opts...)
}

// Stream works like `ping => stream output`.
func (rp *runnablePacker[I, O, TOption]) Stream(ctx context.Context,
	input I, opts ...TOption) (output *schema.StreamReader[O], err error) {

	return rp.s(ctx, input, opts...)
}

// Collect works like `stream input => pong`.
func (rp *runnablePacker[I, O, TOption]) Collect(ctx context.Context,
	input *schema.StreamReader[I], opts ...TOption) (output O, err error) {
	return rp.c(ctx, input, opts...)
}

// Transform works like `stream input => stream output`.
func (rp *runnablePacker[I, O, TOption]) Transform(ctx context.Context,
	input *schema.StreamReader[I], opts ...TOption) (output *schema.StreamReader[O], err error) {
	return rp.t(ctx, input, opts...)
}

func defaultImplConcatStreamReader[T any](
	sr *schema.StreamReader[T], action defaultImplAction) (T, error) {

	c, err := concatStreamReader(sr)
	if err != nil {
		var t T
		return t, newDefaultImplErr(action, streamConcat, err)
	}

	return c, nil
}

func invokeByStream[I, O, TOption any](s Stream[I, O, TOption]) Invoke[I, O, TOption] {
	return func(ctx context.Context, input I, opts ...TOption) (output O, err error) {
		action := actionInvokeByStream

		sr, err := s(ctx, input, opts...)
		if err != nil {
			return output, newDefaultImplErr(action, internalCall, err)
		}

		return defaultImplConcatStreamReader(sr, action)
	}
}

func invokeByCollect[I, O, TOption any](c Collect[I, O, TOption]) Invoke[I, O, TOption] {
	return func(ctx context.Context, input I, opts ...TOption) (output O, err error) {
		action := actionInvokeByCollect

		sr := schema.StreamReaderFromArray([]I{input})

		output, err = c(ctx, sr, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
		}

		return output, err
	}
}

func invokeByTransform[I, O, TOption any](t Transform[I, O, TOption]) Invoke[I, O, TOption] {
	return func(ctx context.Context, input I, opts ...TOption) (output O, err error) {
		action := actionInvokeByTransform

		srInput := schema.StreamReaderFromArray([]I{input})

		srOutput, err := t(ctx, srInput, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
			return output, err
		}

		return defaultImplConcatStreamReader(srOutput, action)
	}
}

func streamByTransform[I, O, TOption any](t Transform[I, O, TOption]) Stream[I, O, TOption] {
	return func(ctx context.Context, input I, opts ...TOption) (output *schema.StreamReader[O], err error) {
		action := actionStreamByTransform

		srInput := schema.StreamReaderFromArray([]I{input})

		output, err = t(ctx, srInput, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
		}

		return output, err
	}
}

func streamByInvoke[I, O, TOption any](i Invoke[I, O, TOption]) Stream[I, O, TOption] {
	return func(ctx context.Context, input I, opts ...TOption) (output *schema.StreamReader[O], err error) {
		action := actionStreamByInvoke

		out, err := i(ctx, input, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)

			return nil, err
		}

		return schema.StreamReaderFromArray([]O{out}), nil
	}
}

func streamByCollect[I, O, TOption any](c Collect[I, O, TOption]) Stream[I, O, TOption] {
	return func(ctx context.Context, input I, opts ...TOption) (output *schema.StreamReader[O], err error) {
		action := actionStreamByCollect

		srInput := schema.StreamReaderFromArray([]I{input})
		out, err := c(ctx, srInput, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)

			return nil, err
		}

		return schema.StreamReaderFromArray([]O{out}), nil
	}
}

func collectByTransform[I, O, TOption any](t Transform[I, O, TOption]) Collect[I, O, TOption] {
	return func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output O, err error) {
		action := actionCollectByTransform

		srOutput, err := t(ctx, input, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)

			return output, err
		}

		return defaultImplConcatStreamReader(srOutput, action)
	}
}

func collectByInvoke[I, O, TOption any](i Invoke[I, O, TOption]) Collect[I, O, TOption] {
	return func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output O, err error) {
		action := actionCollectByInvoke

		in, err := defaultImplConcatStreamReader(input, action)
		if err != nil {
			return output, err
		}

		output, err = i(ctx, in, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
		}

		return output, err
	}
}

func collectByStream[I, O, TOption any](s Stream[I, O, TOption]) Collect[I, O, TOption] {
	return func(ctx context.Context, input *schema.StreamReader[I], opts ...TOption) (output O, err error) {
		action := actionCollectByStream

		in, err := defaultImplConcatStreamReader(input, action)
		if err != nil {
			return output, err
		}

		srOutput, err := s(ctx, in, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)

			return output, err
		}

		return defaultImplConcatStreamReader(srOutput, action)
	}
}

func transformByStream[I, O, TOption any](s Stream[I, O, TOption]) Transform[I, O, TOption] {
	return func(ctx context.Context, input *schema.StreamReader[I],
		opts ...TOption) (output *schema.StreamReader[O], err error) {

		action := actionTransformByStream

		in, err := defaultImplConcatStreamReader(input, action)
		if err != nil {
			return output, err
		}

		output, err = s(ctx, in, opts...)

		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
		}

		return output, err
	}
}

func transformByCollect[I, O, TOption any](c Collect[I, O, TOption]) Transform[I, O, TOption] {
	return func(ctx context.Context, input *schema.StreamReader[I],
		opts ...TOption) (output *schema.StreamReader[O], err error) {

		action := actionTransformByCollect

		out, err := c(ctx, input, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
			return output, err
		}

		return schema.StreamReaderFromArray([]O{out}), nil
	}
}

func transformByInvoke[I, O, TOption any](i Invoke[I, O, TOption]) Transform[I, O, TOption] {
	return func(ctx context.Context, input *schema.StreamReader[I],
		opts ...TOption) (output *schema.StreamReader[O], err error) {

		action := actionTransformByInvoke

		in, err := defaultImplConcatStreamReader(input, action)
		if err != nil {
			return output, err
		}

		out, err := i(ctx, in, opts...)
		if err != nil {
			err = newDefaultImplErr(action, internalCall, err)
			return output, err
		}

		return schema.StreamReaderFromArray([]O{out}), nil
	}
}

func newRunnablePacker[I, O, TOption any](i Invoke[I, O, TOption], s Stream[I, O, TOption],
	c Collect[I, O, TOption], t Transform[I, O, TOption], enableCallback bool) *runnablePacker[I, O, TOption] {

	r := &runnablePacker[I, O, TOption]{}

	if enableCallback {
		if i != nil {
			i = invokeWithCallbacks(i)
		}

		if s != nil {
			s = streamWithCallbacks(s)
		}

		if c != nil {
			c = collectWithCallbacks(c)
		}

		if t != nil {
			t = transformWithCallbacks(t)
		}
	}

	if i != nil {
		r.i = i
	} else if s != nil {
		r.i = invokeByStream(s)
	} else if c != nil {
		r.i = invokeByCollect(c)
	} else {
		r.i = invokeByTransform(t)
	}

	if s != nil {
		r.s = s
	} else if t != nil {
		r.s = streamByTransform(t)
	} else if i != nil {
		r.s = streamByInvoke(i)
	} else {
		r.s = streamByCollect(c)
	}

	if c != nil {
		r.c = c
	} else if t != nil {
		r.c = collectByTransform(t)
	} else if i != nil {
		r.c = collectByInvoke(i)
	} else {
		r.c = collectByStream(s)
	}

	if t != nil {
		r.t = t
	} else if s != nil {
		r.t = transformByStream(s)
	} else if c != nil {
		r.t = transformByCollect(c)
	} else {
		r.t = transformByInvoke(i)
	}

	return r
}

func toGenericRunnable[I, O any](cr *composableRunnable, ctxWrapper func(ctx context.Context, opts ...Option) context.Context) (
	*runnablePacker[I, O, Option], error) {
	i := func(ctx context.Context, input I, opts ...Option) (output O, err error) {
		out, err := cr.i(ctx, input, toAnyList(opts)...)
		if err != nil {
			return output, err
		}

		return out.(O), err
	}

	t := func(ctx context.Context, input *schema.StreamReader[I],
		opts ...Option) (output *schema.StreamReader[O], err error) {
		in := packStreamReader(input)
		out, err := cr.t(ctx, in, toAnyList(opts)...)

		if err != nil {
			return nil, err
		}

		output, ok := unpackStreamReader[O](out)
		if !ok {
			panic("impossible")
		}

		return output, nil
	}

	r := newRunnablePacker(i, nil, nil, t, false)
	r.wrapRunnableCtx(ctxWrapper)

	return r, nil
}

func inputKeyedComposableRunnable(key string, r *composableRunnable) *composableRunnable {
	wrapper := *r
	wrapper.inputConverter.invoke = defaultValueChecker[map[string]any]
	wrapper.inputConverter.transform = defaultStreamConverter[map[string]any]
	i := r.i
	wrapper.i = func(ctx context.Context, input any, opts ...any) (output any, err error) {
		v, ok := input.(map[string]any)[key]
		if !ok {
			return nil, fmt.Errorf("cannot find input key: %s", key)
		}
		out, err := i(ctx, v, opts...)
		if err != nil {
			return nil, err
		}

		return out, nil
	}

	t := r.t
	wrapper.t = func(ctx context.Context, input streamReader, opts ...any) (output streamReader, err error) {
		nInput, ok := r.inputStreamFilter(key, input)
		if !ok {
			return nil, fmt.Errorf("inputStreamFilter failed, key= %s, node name= %s, err= %w", key, r.nodeInfo.name, err)
		}
		out, err := t(ctx, nInput, opts...)
		if err != nil {
			return nil, err
		}

		return out, nil
	}

	wrapper.inputType = generic.TypeOf[map[string]any]()
	return &wrapper
}

func outputKeyedComposableRunnable(key string, r *composableRunnable) *composableRunnable {
	wrapper := *r
	i := r.i
	wrapper.i = func(ctx context.Context, input any, opts ...any) (output any, err error) {
		out, err := i(ctx, input, opts...)
		if err != nil {
			return nil, err
		}

		return map[string]any{key: out}, nil
	}

	t := r.t
	wrapper.t = func(ctx context.Context, input streamReader, opts ...any) (output streamReader, err error) {
		out, err := t(ctx, input, opts...)
		if err != nil {
			return nil, err
		}

		return out.withKey(key), nil
	}

	wrapper.outputType = generic.TypeOf[map[string]any]()

	return &wrapper
}

// composablePassthrough special runnable that passthrough input to output
func composablePassthrough() *composableRunnable {
	r := &composableRunnable{isPassthrough: true, nodeInfo: &nodeInfo{}}

	r.i = func(ctx context.Context, input any, opts ...any) (output any, err error) {
		return input, nil
	}

	r.t = func(ctx context.Context, input streamReader, opts ...any) (output streamReader, err error) {
		return input, nil
	}

	r.meta = &executorMeta{
		component:                  ComponentOfPassthrough,
		isComponentCallbackEnabled: false,
		componentImplType:          "Passthrough",
	}

	return r
}
