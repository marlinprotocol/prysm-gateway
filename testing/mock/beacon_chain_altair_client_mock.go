// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/prysmaticlabs/prysm/proto/prysm/v1alpha1 (interfaces: BeaconChainAltairClient)

// Package mock is a generated GoMock package.
package mock

import (
	context "context"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
	v1alpha1 "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	v2 "github.com/prysmaticlabs/prysm/v3/proto/prysm/v1alpha1"
	grpc "google.golang.org/grpc"
)

// MockBeaconChainAltairClient is a mock of BeaconChainAltairClient interface
type MockBeaconChainAltairClient struct {
	ctrl     *gomock.Controller
	recorder *MockBeaconChainAltairClientMockRecorder
}

// MockBeaconChainAltairClientMockRecorder is the mock recorder for MockBeaconChainAltairClient
type MockBeaconChainAltairClientMockRecorder struct {
	mock *MockBeaconChainAltairClient
}

// NewMockBeaconChainAltairClient creates a new mock instance
func NewMockBeaconChainAltairClient(ctrl *gomock.Controller) *MockBeaconChainAltairClient {
	mock := &MockBeaconChainAltairClient{ctrl: ctrl}
	mock.recorder = &MockBeaconChainAltairClientMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockBeaconChainAltairClient) EXPECT() *MockBeaconChainAltairClientMockRecorder {
	return m.recorder
}

// ListBlocks mocks base method
func (m *MockBeaconChainAltairClient) ListBlocks(arg0 context.Context, arg1 *v1alpha1.ListBlocksRequest, arg2 ...grpc.CallOption) (*v2.ListBlocksResponse, error) {
	m.ctrl.T.Helper()
	varargs := []interface{}{arg0, arg1}
	for _, a := range arg2 {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "ListBlocks", varargs...)
	ret0, _ := ret[0].(*v2.ListBlocksResponse)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ListBlocks indicates an expected call of ListBlocks
func (mr *MockBeaconChainAltairClientMockRecorder) ListBlocks(arg0, arg1 interface{}, arg2 ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]interface{}{arg0, arg1}, arg2...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ListBlocks", reflect.TypeOf((*MockBeaconChainAltairClient)(nil).ListBlocks), varargs...)
}
