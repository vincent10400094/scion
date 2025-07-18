// This file is copied from github.com/hashicorp/vault

// License text copyright (c) 2020 MariaDB Corporation Ab, All Rights Reserved.
// “Business Source License” is a trademark of MariaDB Corporation Ab.

// Parameters

// Licensor:             HashiCorp, Inc.
// Licensed Work:        Vault 1.15.2. The Licensed Work is (c) 2023 HashiCorp, Inc.
// Additional Use Grant: You may make production use of the Licensed Work, provided
//                       Your use does not include offering the Licensed Work to third
//                       parties on a hosted or embedded basis in order to compete with
//                       HashiCorp’s paid version(s) of the Licensed Work. For purposes
//                       of this license:

//                       A “competitive offering” is a Product that is offered to third
//                       parties on a paid basis, including through paid support
//                       arrangements, that significantly overlaps with the capabilities
//                       of HashiCorp’s paid version(s) of the Licensed Work. If Your
//                       Product is not a competitive offering when You first make it
//                       generally available, it will not become a competitive offering
//                       later due to HashiCorp releasing a new version of the Licensed
//                       Work with additional capabilities. In addition, Products that
//                       are not provided on a paid basis are not competitive.

//                       “Product” means software that is offered to end users to manage
//                       in their own environments or offered as a service on a hosted
//                       basis.

//                       “Embedded” means including the source code or executable code
//                       from the Licensed Work in a competitive offering. “Embedded”
//                       also means packaging the competitive offering in such a way
//                       that the Licensed Work must be accessed or downloaded for the
//                       competitive offering to operate.

//                       Hosting or using the Licensed Work(s) for internal purposes
//                       within an organization is not considered a competitive
//                       offering. HashiCorp considers your organization to include all
//                       of your affiliates under common control.

//                       For binding interpretive guidance on using HashiCorp products
//                       under the Business Source License, please visit our FAQ.
//                       (https://www.hashicorp.com/license-faq)
// Change Date:          Four years from the date the Licensed Work is published.
// Change License:       MPL 2.0

// For information about alternative licensing arrangements for the Licensed Work,
// please contact licensing@hashicorp.com.

// Notice

// Business Source License 1.1

// Terms

// The Licensor hereby grants you the right to copy, modify, create derivative
// works, redistribute, and make non-production use of the Licensed Work. The
// Licensor may make an Additional Use Grant, above, permitting limited production use.

// Effective on the Change Date, or the fourth anniversary of the first publicly
// available distribution of a specific version of the Licensed Work under this
// License, whichever comes first, the Licensor hereby grants you rights under
// the terms of the Change License, and the rights granted in the paragraph
// above terminate.

// If your use of the Licensed Work does not comply with the requirements
// currently in effect as described in this License, you must purchase a
// commercial license from the Licensor, its affiliated entities, or authorized
// resellers, or you must refrain from using the Licensed Work.

// All copies of the original and modified Licensed Work, and derivative works
// of the Licensed Work, are subject to this License. This License applies
// separately for each version of the Licensed Work and the Change Date may vary
// for each version of the Licensed Work released by Licensor.

// You must conspicuously display this License on each original or modified copy
// of the Licensed Work. If you receive the Licensed Work in original or
// modified form from a third party, the terms and conditions set forth in this
// License apply to your use of that work.

// Any use of the Licensed Work in violation of this License will automatically
// terminate your rights under this License for the current and all other
// versions of the Licensed Work.

// This License does not grant you any right in any trademark or logo of
// Licensor or its affiliates (provided that you may use a trademark or logo of
// Licensor as expressly required by this License).

// TO THE EXTENT PERMITTED BY APPLICABLE LAW, THE LICENSED WORK IS PROVIDED ON
// AN “AS IS” BASIS. LICENSOR HEREBY DISCLAIMS ALL WARRANTIES AND CONDITIONS,
// EXPRESS OR IMPLIED, INCLUDING (WITHOUT LIMITATION) WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, NON-INFRINGEMENT, AND
// TITLE.

package dataplane

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	mathrand "math/rand"
	"time"
)

const (
	// ShareOverhead is the byte size overhead of each share
	// when using Split on a secret. This is caused by appending
	// a one byte tag to the share.
	ShareOverhead = 1
)

// polynomial represents a polynomial of arbitrary degree
type polynomial struct {
	coefficients []uint8
}

// makePolynomial constructs a random polynomial of the given
// degree but with the provided intercept value.
func makePolynomial(intercept, degree uint8, coefs []byte) (polynomial, error) {
	// Create a wrapper
	p := polynomial{
		coefficients: make([]byte, degree+1),
	}

	// Ensure the intercept is set
	p.coefficients[0] = intercept

	// // Assign random co-efficients to the polynomial
	// if _, err := rand.Read(p.coefficients[1:]); err != nil {
	// 	return p, err
	// }

	copy(p.coefficients[1:], coefs)

	// Assign co-efficients statically to test performance
	// for i := 1; i <= int(degree); i++ {
	// 	p.coefficients[i] = uint8(i)
	// }

	return p, nil
}

// evaluate returns the value of the polynomial for the given x
func (p *polynomial) evaluate(x uint8) uint8 {
	// Special case the origin
	if x == 0 {
		return p.coefficients[0]
	}

	// Compute the polynomial value using Horner's method.
	degree := len(p.coefficients) - 1
	out := p.coefficients[degree]
	for i := degree - 1; i >= 0; i-- {
		coeff := p.coefficients[i]
		out = add(mult(out, x), coeff)
	}
	return out
}

// interpolatePolynomial takes N sample points and returns
// the value at a given x using a lagrange interpolation.
func interpolatePolynomial(x_samples, y_samples []uint8, x uint8) uint8 {
	limit := len(x_samples)
	var result, basis uint8
	for i := 0; i < limit; i++ {
		basis = 1
		for j := 0; j < limit; j++ {
			if i == j {
				continue
			}
			num := add(x, x_samples[j])
			denom := add(x_samples[i], x_samples[j])
			term := div(num, denom)
			basis = mult(basis, term)
		}
		group := mult(y_samples[i], basis)
		result = add(result, group)
	}
	return result
}

// div divides two numbers in GF(2^8)
func div(a, b uint8) uint8 {
	if b == 0 {
		// leaks some timing information but we don't care anyways as this
		// should never happen, hence the panic
		panic("divide by zero")
	}

	var goodVal, zero uint8
	log_a := logTable[a]
	log_b := logTable[b]
	diff := (int(log_a) - int(log_b)) % 255
	if diff < 0 {
		diff += 255
	}

	ret := expTable[diff]

	// Ensure we return zero if a is zero but aren't subject to timing attacks
	goodVal = ret

	if subtle.ConstantTimeByteEq(a, 0) == 1 {
		ret = zero
	} else {
		ret = goodVal
	}

	return ret
}

// mult multiplies two numbers in GF(2^8)
func mult(a, b uint8) (out uint8) {
	var goodVal, zero uint8
	log_a := logTable[a]
	log_b := logTable[b]
	sum := (int(log_a) + int(log_b)) % 255

	ret := expTable[sum]

	// Ensure we return zero if either a or b are zero but aren't subject to
	// timing attacks
	goodVal = ret

	if subtle.ConstantTimeByteEq(a, 0) == 1 {
		ret = zero
	} else {
		ret = goodVal
	}

	if subtle.ConstantTimeByteEq(b, 0) == 1 {
		ret = zero
	} else {
		// This operation does not do anything logically useful. It
		// only ensures a constant number of assignments to thwart
		// timing attacks.
		goodVal = zero
	}

	return ret
}

// add combines two numbers in GF(2^8)
// This can also be used for subtraction since it is symmetric.
func add(a, b uint8) uint8 {
	return a ^ b
}

// Split takes an arbitrarily long secret and generates a `parts`
// number of shares, `threshold` of which are required to reconstruct
// the secret. The parts and threshold must be at least 2, and less
// than 256. The returned shares are each one byte longer than the secret
// as they attach a tag used to reconstruct the secret.
func Split(secret []byte, parts, threshold int) ([][]byte, error) {
	// Sanity check the input
	if parts < threshold {
		return nil, fmt.Errorf("parts cannot be less than threshold")
	}
	if parts > 255 {
		return nil, fmt.Errorf("parts cannot exceed 255")
	}
	if threshold < 2 {
		return nil, fmt.Errorf("threshold must be at least 2")
	}
	if threshold > 255 {
		return nil, fmt.Errorf("threshold cannot exceed 255")
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("cannot split an empty secret")
	}

	// Generate random list of x coordinates
	mathrand.Seed(time.Now().UnixNano())
	xCoordinates := mathrand.Perm(255)

	// Generate the permutation statically for performance
	// xCoordinates := make([]int, 255)
	// for i := 0; i < 255; i++ {
	// 	xCoordinates[i] = 10 + i
	// }

	// Allocate the output array, initialize the final byte
	// of the output with the offset. The representation of each
	// output is {y1, y2, .., yN, x}.
	out := make([][]byte, parts)
	for idx := range out {
		out[idx] = make([]byte, len(secret)+1)
		out[idx][len(secret)] = uint8(xCoordinates[idx]) + 1
	}

	// create polynomial coefficients in a batch
	polyCoefs := make([]byte, len(secret)*(threshold-1))

	n, err := rand.Read(polyCoefs)
	if err != nil {
		return nil, err
	}
	if n != len(polyCoefs) {
		return nil, fmt.Errorf("failed to read enough random bytes")
	}

	// Construct a random polynomial for each byte of the secret.
	// Because we are using a field of size 256, we can only represent
	// a single byte as the intercept of the polynomial, so we must
	// use a new polynomial for each byte.
	for idx, val := range secret {
		p, err := makePolynomial(val, uint8(threshold-1), polyCoefs[idx*(threshold-1):(idx+1)*(threshold-1)])
		if err != nil {
			return nil, err
		}

		// Generate a `parts` number of (x,y) pairs
		// We cheat by encoding the x value once as the final index,
		// so that it only needs to be stored once.
		for i := 0; i < parts; i++ {
			x := uint8(xCoordinates[i]) + 1
			y := p.evaluate(x)
			out[i][idx] = y
		}
	}

	// Return the encoded secrets
	return out, nil
}

// Combine is used to reverse a Split and reconstruct a secret
// once a `threshold` number of parts are available.
func Combine(parts [][]byte) ([]byte, error) {
	// Verify enough parts provided
	if len(parts) < 2 {
		return nil, fmt.Errorf("less than two parts cannot be used to reconstruct the secret")
	}

	// Verify the parts are all the same length
	firstPartLen := len(parts[0])
	if firstPartLen < 2 {
		return nil, fmt.Errorf("parts must be at least two bytes")
	}
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) != firstPartLen {
			return nil, fmt.Errorf("all parts must be the same length")
		}
	}

	// Create a buffer to store the reconstructed secret
	secret := make([]byte, firstPartLen-1)

	// Buffer to store the samples
	x_samples := make([]uint8, len(parts))
	y_samples := make([]uint8, len(parts))

	// Set the x value for each sample and ensure no x_sample values are the same,
	// otherwise div() can be unhappy
	checkMap := map[byte]bool{}
	for i, part := range parts {
		samp := part[firstPartLen-1]
		if exists := checkMap[samp]; exists {
			return nil, fmt.Errorf("duplicate part detected")
		}
		checkMap[samp] = true
		x_samples[i] = samp
	}

	// Reconstruct each byte
	for idx := range secret {
		// Set the y value for each sample
		for i, part := range parts {
			y_samples[i] = part[idx]
		}

		// Interpolate the polynomial and compute the value at 0
		val := interpolatePolynomial(x_samples, y_samples, 0)

		// Evaluate the 0th value to get the intercept
		secret[idx] = val
	}
	return secret, nil
}

var (
	// logTable provides the log(X)/log(g) at each index X
	logTable = [256]uint8{
		0x00, 0xff, 0xc8, 0x08, 0x91, 0x10, 0xd0, 0x36,
		0x5a, 0x3e, 0xd8, 0x43, 0x99, 0x77, 0xfe, 0x18,
		0x23, 0x20, 0x07, 0x70, 0xa1, 0x6c, 0x0c, 0x7f,
		0x62, 0x8b, 0x40, 0x46, 0xc7, 0x4b, 0xe0, 0x0e,
		0xeb, 0x16, 0xe8, 0xad, 0xcf, 0xcd, 0x39, 0x53,
		0x6a, 0x27, 0x35, 0x93, 0xd4, 0x4e, 0x48, 0xc3,
		0x2b, 0x79, 0x54, 0x28, 0x09, 0x78, 0x0f, 0x21,
		0x90, 0x87, 0x14, 0x2a, 0xa9, 0x9c, 0xd6, 0x74,
		0xb4, 0x7c, 0xde, 0xed, 0xb1, 0x86, 0x76, 0xa4,
		0x98, 0xe2, 0x96, 0x8f, 0x02, 0x32, 0x1c, 0xc1,
		0x33, 0xee, 0xef, 0x81, 0xfd, 0x30, 0x5c, 0x13,
		0x9d, 0x29, 0x17, 0xc4, 0x11, 0x44, 0x8c, 0x80,
		0xf3, 0x73, 0x42, 0x1e, 0x1d, 0xb5, 0xf0, 0x12,
		0xd1, 0x5b, 0x41, 0xa2, 0xd7, 0x2c, 0xe9, 0xd5,
		0x59, 0xcb, 0x50, 0xa8, 0xdc, 0xfc, 0xf2, 0x56,
		0x72, 0xa6, 0x65, 0x2f, 0x9f, 0x9b, 0x3d, 0xba,
		0x7d, 0xc2, 0x45, 0x82, 0xa7, 0x57, 0xb6, 0xa3,
		0x7a, 0x75, 0x4f, 0xae, 0x3f, 0x37, 0x6d, 0x47,
		0x61, 0xbe, 0xab, 0xd3, 0x5f, 0xb0, 0x58, 0xaf,
		0xca, 0x5e, 0xfa, 0x85, 0xe4, 0x4d, 0x8a, 0x05,
		0xfb, 0x60, 0xb7, 0x7b, 0xb8, 0x26, 0x4a, 0x67,
		0xc6, 0x1a, 0xf8, 0x69, 0x25, 0xb3, 0xdb, 0xbd,
		0x66, 0xdd, 0xf1, 0xd2, 0xdf, 0x03, 0x8d, 0x34,
		0xd9, 0x92, 0x0d, 0x63, 0x55, 0xaa, 0x49, 0xec,
		0xbc, 0x95, 0x3c, 0x84, 0x0b, 0xf5, 0xe6, 0xe7,
		0xe5, 0xac, 0x7e, 0x6e, 0xb9, 0xf9, 0xda, 0x8e,
		0x9a, 0xc9, 0x24, 0xe1, 0x0a, 0x15, 0x6b, 0x3a,
		0xa0, 0x51, 0xf4, 0xea, 0xb2, 0x97, 0x9e, 0x5d,
		0x22, 0x88, 0x94, 0xce, 0x19, 0x01, 0x71, 0x4c,
		0xa5, 0xe3, 0xc5, 0x31, 0xbb, 0xcc, 0x1f, 0x2d,
		0x3b, 0x52, 0x6f, 0xf6, 0x2e, 0x89, 0xf7, 0xc0,
		0x68, 0x1b, 0x64, 0x04, 0x06, 0xbf, 0x83, 0x38}

	// expTable provides the anti-log or exponentiation value
	// for the equivalent index
	expTable = [256]uint8{
		0x01, 0xe5, 0x4c, 0xb5, 0xfb, 0x9f, 0xfc, 0x12,
		0x03, 0x34, 0xd4, 0xc4, 0x16, 0xba, 0x1f, 0x36,
		0x05, 0x5c, 0x67, 0x57, 0x3a, 0xd5, 0x21, 0x5a,
		0x0f, 0xe4, 0xa9, 0xf9, 0x4e, 0x64, 0x63, 0xee,
		0x11, 0x37, 0xe0, 0x10, 0xd2, 0xac, 0xa5, 0x29,
		0x33, 0x59, 0x3b, 0x30, 0x6d, 0xef, 0xf4, 0x7b,
		0x55, 0xeb, 0x4d, 0x50, 0xb7, 0x2a, 0x07, 0x8d,
		0xff, 0x26, 0xd7, 0xf0, 0xc2, 0x7e, 0x09, 0x8c,
		0x1a, 0x6a, 0x62, 0x0b, 0x5d, 0x82, 0x1b, 0x8f,
		0x2e, 0xbe, 0xa6, 0x1d, 0xe7, 0x9d, 0x2d, 0x8a,
		0x72, 0xd9, 0xf1, 0x27, 0x32, 0xbc, 0x77, 0x85,
		0x96, 0x70, 0x08, 0x69, 0x56, 0xdf, 0x99, 0x94,
		0xa1, 0x90, 0x18, 0xbb, 0xfa, 0x7a, 0xb0, 0xa7,
		0xf8, 0xab, 0x28, 0xd6, 0x15, 0x8e, 0xcb, 0xf2,
		0x13, 0xe6, 0x78, 0x61, 0x3f, 0x89, 0x46, 0x0d,
		0x35, 0x31, 0x88, 0xa3, 0x41, 0x80, 0xca, 0x17,
		0x5f, 0x53, 0x83, 0xfe, 0xc3, 0x9b, 0x45, 0x39,
		0xe1, 0xf5, 0x9e, 0x19, 0x5e, 0xb6, 0xcf, 0x4b,
		0x38, 0x04, 0xb9, 0x2b, 0xe2, 0xc1, 0x4a, 0xdd,
		0x48, 0x0c, 0xd0, 0x7d, 0x3d, 0x58, 0xde, 0x7c,
		0xd8, 0x14, 0x6b, 0x87, 0x47, 0xe8, 0x79, 0x84,
		0x73, 0x3c, 0xbd, 0x92, 0xc9, 0x23, 0x8b, 0x97,
		0x95, 0x44, 0xdc, 0xad, 0x40, 0x65, 0x86, 0xa2,
		0xa4, 0xcc, 0x7f, 0xec, 0xc0, 0xaf, 0x91, 0xfd,
		0xf7, 0x4f, 0x81, 0x2f, 0x5b, 0xea, 0xa8, 0x1c,
		0x02, 0xd1, 0x98, 0x71, 0xed, 0x25, 0xe3, 0x24,
		0x06, 0x68, 0xb3, 0x93, 0x2c, 0x6f, 0x3e, 0x6c,
		0x0a, 0xb8, 0xce, 0xae, 0x74, 0xb1, 0x42, 0xb4,
		0x1e, 0xd3, 0x49, 0xe9, 0x9c, 0xc8, 0xc6, 0xc7,
		0x22, 0x6e, 0xdb, 0x20, 0xbf, 0x43, 0x51, 0x52,
		0x66, 0xb2, 0x76, 0x60, 0xda, 0xc5, 0xf3, 0xf6,
		0xaa, 0xcd, 0x9a, 0xa0, 0x75, 0x54, 0x0e, 0x01}
)
