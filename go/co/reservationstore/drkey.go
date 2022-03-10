// Copyright 2021 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reservationstore

import (
	"context"
	"crypto/subtle"
	"encoding/hex"

	base "github.com/scionproto/scion/go/co/reservation"
	"github.com/scionproto/scion/go/co/reservation/e2e"
	"github.com/scionproto/scion/go/co/reservation/segment"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/daemon"
	"github.com/scionproto/scion/go/lib/drkey"
	drkut "github.com/scionproto/scion/go/lib/drkey/drkeyutil"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
)

type Authenticator interface {
	macComputer
	macVerifier
}

type macComputer interface {
	// ComputeRequestInitialMAC computes the MAC for the immutable fields of the basic request,
	// for each AS in transit. This MAC is only computed at the first AS.
	// The initial AS is obtained from the first step of the path of the request.
	ComputeRequestInitialMAC(ctx context.Context, req *base.Request) error
	// SegmentRequestInitialMAC computes the MAC for the immutable fields of the setup request,
	// for each AS in transit. This MAC is only computed at the first AS.
	// The initial AS is obtained from the first step of the path of the request.
	ComputeSegmentSetupRequestInitialMAC(ctx context.Context, req *segment.SetupReq) error
	ComputeRequestTransitMAC(ctx context.Context, req *base.Request) error

	ComputeSegmentSetupRequestTransitMAC(ctx context.Context, req *segment.SetupReq) error
	ComputeE2ERequestTransitMAC(ctx context.Context, req *e2e.Request) error
	ComputeE2ESetupRequestTransitMAC(ctx context.Context, req *e2e.SetupReq) error

	// ComputeResponseMAC takes the response (passed as an interface here) and computes and sets
	// the authenticators inside it.
	// These authenticators will be later validated at the source end-host.
	ComputeResponseMAC(ctx context.Context, res base.Response, path *base.TransparentPath) error
	ComputeSegmentSetupResponseMAC(ctx context.Context, res segment.SegmentSetupResponse,
		path *base.TransparentPath) error
	ComputeE2EResponseMAC(ctx context.Context, res base.Response, path *base.TransparentPath,
		srcHost addr.HostAddr) error
	ComputeE2ESetupResponseMAC(ctx context.Context, res e2e.SetupResponse,
		path *base.TransparentPath, srcHost addr.HostAddr, rsvID *reservation.ID) error
}

type macVerifier interface {
	// ValidateRequest verifies the validity of the source authentication
	// created by the initial AS for this particular transit AS as, for the immutable parts of
	// this request. If the request is now at the last AS, it also validates the request at
	// the destination. Returns true if valid, false otherwise.
	ValidateRequest(ctx context.Context, req *base.Request) (bool, error)
	// ValidateSegSetupRequest verifies the validity of the source authentication
	// created by the initial AS for this particular transit AS as, for the immutable parts of
	// this request. If the request is now at the last AS, it also validates the request at
	// the destination. Returns true if valid, false otherwise.
	ValidateSegSetupRequest(ctx context.Context, req *segment.SetupReq) (bool, error)
	// Validates a basic E2E request while in a transit AS.
	// The authenticators were created on the source host.
	ValidateE2ERequest(ctx context.Context, req *e2e.Request) (bool, error)
	// ValidateE2ESetupRequest verifies the validity of the source authentication
	// created by the initial AS for this particular transit AS as, for the immutable parts of
	// this request. If the request is now at the last AS, it also validates the request at
	// the destination. Returns true if valid, false otherwise.
	ValidateE2ESetupRequest(ctx context.Context, req *e2e.SetupReq) (bool, error)

	ValidateResponse(ctx context.Context, res base.Response,
		path *base.TransparentPath) (bool, error)
	ValidateSegmentSetupResponse(ctx context.Context,
		res segment.SegmentSetupResponse, path *base.TransparentPath) (bool, error)
}

// DRKeyAuthenticator implements macComputer and macVerifier using DRKey.
type DRKeyAuthenticator struct {
	localIA   addr.IA
	connector daemon.Connector // to obtain level 1 & 2 keys
}

func NewDRKeyAuthenticator(localIA addr.IA, connector daemon.Connector) Authenticator {
	return &DRKeyAuthenticator{
		localIA:   localIA,
		connector: connector,
	}
}

func (a *DRKeyAuthenticator) ComputeRequestInitialMAC(ctx context.Context,
	req *base.Request) error {

	payload := inputInitialBaseRequest(req)
	return a.computeInitialMACforPayloadWithSegKeys(ctx, payload, req)
}

func (a *DRKeyAuthenticator) ComputeSegmentSetupRequestInitialMAC(ctx context.Context,
	req *segment.SetupReq) error {

	payload := inputInitialSegSetupRequest(req)
	return a.computeInitialMACforPayloadWithSegKeys(ctx, payload, &req.Request)
}

func (a *DRKeyAuthenticator) ComputeRequestTransitMAC(ctx context.Context,
	req *base.Request) error {

	if req.IsFirstAS() || req.IsLastAS() {
		return nil
	}
	payload := inputTransitSegRequest(req)
	return a.computeTransitMACforPayload(ctx, payload, req)
}

func (a *DRKeyAuthenticator) ComputeSegmentSetupRequestTransitMAC(ctx context.Context,
	req *segment.SetupReq) error {

	if req.IsFirstAS() || req.IsLastAS() {
		return nil
	}
	payload := inputTransitSegSetupRequest(req)
	return a.computeTransitMACforPayload(ctx, payload, &req.Request)
}

func (a *DRKeyAuthenticator) ComputeE2ERequestTransitMAC(ctx context.Context,
	req *e2e.Request) error {

	if req.IsFirstAS() || req.IsLastAS() {
		return nil
	}
	payload := inputTransitE2ERequest(req)
	return a.computeTransitMACforE2EPayload(ctx, payload, req)
}

func (a *DRKeyAuthenticator) ComputeE2ESetupRequestTransitMAC(ctx context.Context,
	req *e2e.SetupReq) error {

	if req.IsFirstAS() || req.IsLastAS() {
		return nil
	}
	payload := inputTransitE2ESetupRequest(req)
	return a.computeTransitMACforE2EPayload(ctx, payload, &req.Request)
}

func (a *DRKeyAuthenticator) ComputeResponseMAC(ctx context.Context,
	res base.Response, path *base.TransparentPath) error {

	key, err := a.getDRKeyAS2AS(ctx, a.localIA, path.SrcIA())
	if err != nil {
		return err
	}
	payload := res.ToRaw()
	mac, err := MAC(payload, key)
	if err != nil {
		return err
	}
	res.SetAuthenticator(path.CurrentStep, mac)
	return nil
}

func (a *DRKeyAuthenticator) ComputeSegmentSetupResponseMAC(ctx context.Context,
	res segment.SegmentSetupResponse, path *base.TransparentPath) error {

	key, err := a.getDRKeyAS2AS(ctx, a.localIA, path.SrcIA())
	if err != nil {
		return err
	}
	payload := res.ToRawAllHFs()
	mac, err := MAC(payload, key)
	if err != nil {
		return err
	}
	res.SetAuthenticator(path.CurrentStep, mac)
	return nil
}

func (a *DRKeyAuthenticator) ComputeE2EResponseMAC(ctx context.Context, res base.Response,
	path *base.TransparentPath, srcHost addr.HostAddr) error {

	key, err := a.getDRKeyAS2Host(ctx, a.localIA, path.SrcIA(), srcHost)
	if err != nil {
		return err
	}
	payload := res.ToRaw()
	mac, err := MAC(payload, key)
	if err != nil {
		return err
	}
	// because base.Response.SetAuthenticator will use step-1 for the auth position, but we
	// actually want the [step] position, add one:
	res.SetAuthenticator(path.CurrentStep+1, mac)
	return nil
}

func (a *DRKeyAuthenticator) ComputeE2ESetupResponseMAC(ctx context.Context, res e2e.SetupResponse,
	path *base.TransparentPath, srcHost addr.HostAddr, rsvID *reservation.ID) error {

	key, err := a.getDRKeyAS2Host(ctx, a.localIA, path.SrcIA(), srcHost)
	if err != nil {
		return err
	}
	payload, err := res.ToRaw(path.CurrentStep, rsvID)
	if err != nil {
		return err
	}
	mac, err := MAC(payload, key)
	if err != nil {
		return err
	}
	res.SetAuthenticator(path.CurrentStep, mac)
	return nil
}

func (a *DRKeyAuthenticator) ValidateRequest(ctx context.Context,
	req *base.Request) (bool, error) {

	immutableInput := make([]byte, req.Len())
	req.Serialize(immutableInput, base.SerializeImmutable)

	ok, err := a.validateSegmentPayloadInitialMAC(ctx, req, immutableInput)
	if err == nil && ok && req.IsLastAS() {
		ok, err = a.validateRequestAtDestination(ctx, req)
	}
	return ok, err
}

func (a *DRKeyAuthenticator) ValidateSegSetupRequest(ctx context.Context,
	req *segment.SetupReq) (bool, error) {

	if req.IsFirstAS() {
		return true, nil
	}
	ok, err := a.validateSegmentPayloadInitialMAC(ctx, &req.Request,
		inputInitialSegSetupRequest(req))
	if err == nil && ok && req.IsLastAS() {
		ok, err = a.validateSegmentSetupRequestAtDestination(ctx, req)
	}
	return ok, err
}

func (a *DRKeyAuthenticator) ValidateE2ERequest(ctx context.Context, req *e2e.Request) (
	bool, error) {

	if req.IsFirstAS() {
		return true, nil
	}
	payload := make([]byte, req.Len())
	req.Serialize(payload, base.SerializeImmutable)

	ok, err := a.validateE2EPayloadInitialMAC(ctx, req, payload)
	if err == nil && ok && req.IsLastAS() {
		ok, err = a.validateE2ERequestAtDestination(ctx, req)
	}

	return ok, err
}

func (a *DRKeyAuthenticator) ValidateE2ESetupRequest(ctx context.Context,
	req *e2e.SetupReq) (bool, error) {

	if req.IsFirstAS() {
		return true, nil
	}
	payload := make([]byte, req.Len())
	req.Serialize(payload, base.SerializeImmutable)

	ok, err := a.validateE2EPayloadInitialMAC(ctx, &req.Request, payload)
	if err == nil && ok && req.IsLastAS() {
		ok, err = a.validateE2ESetupRequestAtDestination(ctx, req)
	}
	return ok, err

}

func (a *DRKeyAuthenticator) ValidateResponse(ctx context.Context, res base.Response,
	path *base.TransparentPath) (bool, error) {

	keys, err := a.slowAS2ASFromPath(ctx, path.Steps)
	if err != nil {
		return false, err
	}
	payload := res.ToRaw()
	return validateAuthenticators(keys, res.GetAuthenticators(), func(int) []byte {
		return payload
	})
}

func (a *DRKeyAuthenticator) ValidateSegmentSetupResponse(ctx context.Context,
	res segment.SegmentSetupResponse, path *base.TransparentPath) (bool, error) {

	stepsLength := len(path.Steps)
	if failure, ok := res.(*segment.SegmentSetupResponseFailure); ok {
		// for failure responses, we can only check the validity from the failing node to
		// the initiator node, as the ones that succeed were using a different response to
		// compute the authenticators.
		stepsLength = int(failure.FailedStep)
	} else if success, ok := res.(*segment.SegmentSetupResponseSuccess); ok {
		assert(len(success.Token.HopFields) == len(path.Steps),
			"inconsistent lengths HFs=%d and steps=%d", len(success.Token.HopFields),
			len(path.Steps))
	}
	if stepsLength == 0 {
		log.Debug("at validateSegmentSetupResponse: no steps to validate (steps_length==0)")
		return true, nil
	}

	keys, err := a.slowAS2ASFromPath(ctx, path.Steps[:stepsLength]) // returns stepsLength -1 keys
	if err != nil {
		return false, err
	}

	return validateAuthenticators(keys, res.GetAuthenticators()[:stepsLength-1],
		func(step int) []byte {
			return res.ToRaw(step)
		})
}

func (a *DRKeyAuthenticator) validateRequestAtDestination(ctx context.Context, req *base.Request) (
	bool, error) {

	return a.validateAtDestination(ctx, req, func(i int) []byte {
		return inputTransitSegRequest(req)
	})
}

func (a *DRKeyAuthenticator) validateSegmentSetupRequestAtDestination(ctx context.Context,
	req *segment.SetupReq) (bool, error) {

	return a.validateAtDestination(ctx, &req.Request, func(step int) []byte {
		return inputTransitSegSetupRequestForStep(req, step)
	})
}

func (a *DRKeyAuthenticator) validateE2ERequestAtDestination(ctx context.Context,
	req *e2e.Request) (bool, error) {

	return a.validateAtDestination(ctx, &req.Request, func(step int) []byte {
		return inputTransitE2ERequest(req)
	})
}

func (a *DRKeyAuthenticator) validateE2ESetupRequestAtDestination(ctx context.Context,
	req *e2e.SetupReq) (bool, error) {

	return a.validateAtDestination(ctx, &req.Request.Request, func(step int) []byte {
		return inputTransitE2ESetupRequestForStep(req, step)
	})
}

func (a *DRKeyAuthenticator) validateSegmentPayloadInitialMAC(ctx context.Context,
	req *base.Request, immutableInput []byte) (bool, error) {

	key, err := a.getDRKeyAS2AS(ctx, a.localIA, req.Path.SrcIA())
	if err != nil {
		return false, serrors.WrapStr("obtaining drkey", err, "fast", a.localIA,
			"slow", req.Path.SrcIA())
	}
	mac, err := MAC(immutableInput, key)
	if err != nil {
		return false, serrors.WrapStr("validating segment initial request", err)
	}
	res := subtle.ConstantTimeCompare(mac, req.CurrentValidatorField())
	if res != 1 {
		log.Info("source authentication failed", "id", req.ID,
			"fast_side", a.localIA,
			"slow_side", req.Path.SrcIA(), "mac", hex.EncodeToString(mac),
			"expected", hex.EncodeToString(req.CurrentValidatorField()))
		return false, nil
	}
	return true, nil
}

// validateE2EPayloadInitialMAC obtains the (fast side this) key according to req.Path and
// uses them to compute the MAC from payload and compare it with the current req.Authenticators.
func (a *DRKeyAuthenticator) validateE2EPayloadInitialMAC(ctx context.Context,
	req *e2e.Request, immutableInput []byte) (bool, error) {

	key, err := a.getDRKeyAS2Host(ctx, a.localIA, req.Path.SrcIA(), addr.HostFromIP(req.SrcHost))
	if err != nil {
		return false, serrors.WrapStr("obtaining drkey", err, "fast", a.localIA,
			"slow_ia", req.Path.SrcIA(), "slow_host", req.SrcHost)
	}

	mac, err := MAC(immutableInput, key)
	if err != nil {
		return false, serrors.WrapStr("validating e2e initial request", err)
	}
	res := subtle.ConstantTimeCompare(mac, req.CurrentValidatorField())
	if res != 1 {
		log.Info("source authentication failed", "id", req.ID,
			"fast_side", a.localIA,
			"slow_ia", req.Path.SrcIA(), "slow_host", req.SrcHost,
			"mac", hex.EncodeToString(mac),
			"expected", hex.EncodeToString(req.CurrentValidatorField()))
		return false, nil
	}
	return true, nil
}

// validateAtDestination validates the authenticators created in-transit. The first
// authenticator, authenticators[0], is created by the second in-path AS. The last
// authenticator, authenticator[n-1], should have been created by the destination AS,
// but since there is no need to authenticate it to itself, it's left empty.
// payloadFcn takes the index of the path step we want to compute the payload for.
func (a *DRKeyAuthenticator) validateAtDestination(ctx context.Context, req *base.Request,
	payloadFcn func(int) []byte) (bool, error) {

	if len(req.Authenticators) != len(req.Path.Steps)-1 {
		return false, serrors.New("insconsistent length in request",
			"auth_count", len(req.Authenticators), "step_count", len(req.Path.Steps))
	}
	keys, err := a.slowAS2ASFromPath(ctx, req.Path.Steps[:len(req.Path.Steps)-1])
	if err != nil {
		return false, serrors.WrapStr("source authentication failed", err, "id", req.ID)
	}
	// we have 1 less key than authenticators (we don't want to validate the last authenticator,
	// as it is the place of the destination AS, which is this one)
	return validateAuthenticators(keys, req.Authenticators[:len(req.Authenticators)-1],
		payloadFcn)
}

func validateAuthenticators(keys [][]byte, authenticators [][]byte,
	payloadFcn func(step int) []byte) (bool, error) {

	if len(authenticators) != len(keys) {
		return false, serrors.New("insconsistent length",
			"auth_count", len(authenticators), "key_count", len(keys))
	}
	for i := 0; i < len(authenticators); i++ {
		payload := payloadFcn(i + 1)
		mac, err := MAC(payload, keys[i])
		if err != nil {
			return false, serrors.WrapStr("computing mac validating source at destination", err)
		}
		res := subtle.ConstantTimeCompare(mac, authenticators[i])
		if res != 1 {
			log.Info("source authentication failed",
				"step", i,
				"mac", hex.EncodeToString(mac),
				"expected", hex.EncodeToString(authenticators[i]))
			return false, nil
		}
	}
	return true, nil
}

func (a *DRKeyAuthenticator) computeInitialMACforPayloadWithSegKeys(ctx context.Context,
	payload []byte, req *base.Request) error {

	keys, err := a.slowAS2ASFromPath(ctx, req.Path.Steps)
	if err != nil {
		return err
	}
	return a.computeInitialMACforPayload(ctx, payload, req, keys)
}

func (a *DRKeyAuthenticator) computeInitialMACforPayload(ctx context.Context, payload []byte,
	req *base.Request, keys [][]byte) error {

	assert(len(keys) == len(req.Path.Steps)-1, "bad key set with length %d (should be %d)",
		len(keys), len(req.Path.Steps)-1)
	var err error
	for i := 0; i < len(req.Path.Steps)-1; i++ {
		req.Authenticators[i], err = MAC(payload, keys[i])
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *DRKeyAuthenticator) computeTransitMACforPayload(ctx context.Context, payload []byte,
	req *base.Request) error {

	key, err := a.getDRKeyAS2AS(ctx, a.localIA, req.Path.DstIA())
	if err != nil {
		return err
	}
	req.Authenticators[req.Path.CurrentStep-1], err = MAC(payload, key)
	return err
}

func (a *DRKeyAuthenticator) computeTransitMACforE2EPayload(ctx context.Context, payload []byte,
	req *e2e.Request) error {

	key, err := a.getDRKeyAS2AS(ctx, a.localIA, req.Path.DstIA())
	if err != nil {
		return err
	}
	req.Authenticators[req.Path.CurrentStep-1], err = MAC(payload, key)
	return err
}

// slowLvl1FromPath gets the L1 keys from the slow side to all ASes in the path.
// Note: this is the slow side.
func (a *DRKeyAuthenticator) slowAS2ASFromPath(ctx context.Context, steps []base.PathStep) (
	[][]byte, error) {

	return a.slowKeysFromPath(ctx, steps, func(ctx context.Context, fast addr.IA) ([]byte, error) {
		return a.getDRKeyAS2AS(ctx, fast, a.localIA)
	})
}

// slowKeysFromPath retrieves the drkeys specified in the steps[1]..steps[n-1]. It skips the
// first step as it is the initiator. The IAs in the steps are used as the fast side of the
// drkeys, and the function `getKeyWithFastSide` is called with them, to retrieve the drkeys.
func (a *DRKeyAuthenticator) slowKeysFromPath(ctx context.Context, steps []base.PathStep,
	getKeyWithFastSide func(ctx context.Context, fast addr.IA) ([]byte, error)) ([][]byte, error) {

	seen := make(map[addr.IA]struct{})
	keys := make([][]byte, len(steps)-1)
	for i := 0; i < len(steps)-1; i++ {
		step := steps[i+1]
		if step.IA.Equal(a.localIA) {
			return nil, serrors.New("request path contains initiator after first step",
				"steps", base.StepsToString(steps))
		}
		if _, ok := seen[step.IA]; ok {
			return nil, serrors.New("IA is twice in request path", "ia", step.IA,
				"steps", base.StepsToString(steps))
		}
		seen[step.IA] = struct{}{}
		key, err := getKeyWithFastSide(ctx, step.IA)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}
	return keys, nil
}

func (a *DRKeyAuthenticator) getDRKeyAS2AS(ctx context.Context, fast, slow addr.IA) (
	[]byte, error) {

	keys, err := drkut.GetLvl2Keys(ctx, a.connector, drkey.AS2AS, "colibri",
		drkut.SlowIAs(slow), drkut.FastIAs(fast))
	return keys[0], err
}

func (a *DRKeyAuthenticator) getDRKeyAS2Host(ctx context.Context, fast, slowIA addr.IA,
	slowHost addr.HostAddr) ([]byte, error) {

	keys, err := drkut.GetLvl2Keys(ctx, a.connector, drkey.AS2Host, "colibri",
		drkut.SlowIAs(slowIA), drkut.SlowHosts(slowHost), drkut.FastIAs(fast))
	return keys[0], err
}

func inputInitialBaseRequest(req *base.Request) []byte {
	buff := make([]byte, req.Len())
	req.Serialize(buff, base.SerializeImmutable)
	return buff
}

func inputInitialSegSetupRequest(req *segment.SetupReq) []byte {
	buff := make([]byte, req.Len())
	req.Serialize(buff, base.SerializeImmutable)
	return buff
}

func inputTransitSegRequest(req *base.Request) []byte {
	buff := make([]byte, req.Len())
	req.Serialize(buff, base.SerializeImmutable)
	return buff
}

func inputTransitSegSetupRequest(req *segment.SetupReq) []byte {
	buff := make([]byte, req.Len()+len(req.AllocTrail)*2)
	req.Serialize(buff, base.SerializeSemiMutable)
	return buff
}

// inputTransitSegSetupRequestForStep is used by the validation of the segment setup request at
// destination. The validation function needs to get the semi mutable payload per AS in the trail,
// thus different ASes will yield different payloads.
func inputTransitSegSetupRequestForStep(req *segment.SetupReq, step int) []byte {
	buff := inputTransitSegSetupRequest(req)
	remainingSteps := len(req.AllocTrail) - step - 1
	return buff[:len(buff)-remainingSteps*2]
}

func inputTransitE2ERequest(req *e2e.Request) []byte {
	buff := make([]byte, req.Len())
	req.Serialize(buff, base.SerializeSemiMutable)
	return buff
}

func inputTransitE2ESetupRequest(req *e2e.SetupReq) []byte {
	buff := make([]byte, req.Len()+len(req.AllocationTrail))
	req.Serialize(buff, base.SerializeSemiMutable)
	return buff
}

// inputTransitE2ESetupRequestForStep serializes the semi mutable fields of req as if it
// were located at step `step`.
func inputTransitE2ESetupRequestForStep(req *e2e.SetupReq, step int) []byte {
	buff := inputTransitE2ESetupRequest(req)
	remainingSteps := len(req.AllocationTrail) - step - 1
	return buff[:len(buff)-remainingSteps]
}

func MAC(payload, key []byte) ([]byte, error) {
	return drkut.MAC(payload, key)
}
