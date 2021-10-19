// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.23.0
// 	protoc        v3.17.3
// source: v1/gateway.proto

package v1

import (
	proto "github.com/golang/protobuf/proto"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// This is a compile-time assertion that a sufficiently up-to-date version
// of the legacy proto package is being used.
const _ = proto.ProtoPackageIsVersion4

// Status identifies gateway connection status
type Gateway_GatewayStatus int32

const (
	// CONNECTED means that the gateway connection is open
	Gateway_CONNECTED Gateway_GatewayStatus = 0
	// DISCONNECTED means that the gateway connection is offline
	Gateway_DISCONNECTED Gateway_GatewayStatus = 1
)

// Enum value maps for Gateway_GatewayStatus.
var (
	Gateway_GatewayStatus_name = map[int32]string{
		0: "CONNECTED",
		1: "DISCONNECTED",
	}
	Gateway_GatewayStatus_value = map[string]int32{
		"CONNECTED":    0,
		"DISCONNECTED": 1,
	}
)

func (x Gateway_GatewayStatus) Enum() *Gateway_GatewayStatus {
	p := new(Gateway_GatewayStatus)
	*p = x
	return p
}

func (x Gateway_GatewayStatus) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (Gateway_GatewayStatus) Descriptor() protoreflect.EnumDescriptor {
	return file_v1_gateway_proto_enumTypes[0].Descriptor()
}

func (Gateway_GatewayStatus) Type() protoreflect.EnumType {
	return &file_v1_gateway_proto_enumTypes[0]
}

func (x Gateway_GatewayStatus) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use Gateway_GatewayStatus.Descriptor instead.
func (Gateway_GatewayStatus) EnumDescriptor() ([]byte, []int) {
	return file_v1_gateway_proto_rawDescGZIP(), []int{0, 0}
}

type Gateway struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// uri is the gateway uri
	Uri string `protobuf:"bytes,1,opt,name=uri,proto3" json:"uri,omitempty"`
	// resourcename is the name of the Teleport resource
	ResourceName string `protobuf:"bytes,2,opt,name=resource_name,json=resourceName,proto3" json:"resource_name,omitempty"`
	// localaddress is the gateway address on localhost
	LocalAddress string `protobuf:"bytes,3,opt,name=local_address,json=localAddress,proto3" json:"local_address,omitempty"`
	// localport is the gateway address on localhost
	LocalPort string `protobuf:"bytes,4,opt,name=local_port,json=localPort,proto3" json:"local_port,omitempty"`
	// protocol is the gateway protocol
	Protocol string `protobuf:"bytes,5,opt,name=protocol,proto3" json:"protocol,omitempty"`
	// hostid is th4e cluster name
	HostId string `protobuf:"bytes,6,opt,name=host_id,json=hostId,proto3" json:"host_id,omitempty"`
	// clusterid is the resource name (db)
	ClusterId string `protobuf:"bytes,7,opt,name=cluster_id,json=clusterId,proto3" json:"cluster_id,omitempty"`
	// status is the gateway status
	Status Gateway_GatewayStatus `protobuf:"varint,8,opt,name=status,proto3,enum=teleport.terminal.v1.Gateway_GatewayStatus" json:"status,omitempty"`
}

func (x *Gateway) Reset() {
	*x = Gateway{}
	if protoimpl.UnsafeEnabled {
		mi := &file_v1_gateway_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Gateway) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Gateway) ProtoMessage() {}

func (x *Gateway) ProtoReflect() protoreflect.Message {
	mi := &file_v1_gateway_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Gateway.ProtoReflect.Descriptor instead.
func (*Gateway) Descriptor() ([]byte, []int) {
	return file_v1_gateway_proto_rawDescGZIP(), []int{0}
}

func (x *Gateway) GetUri() string {
	if x != nil {
		return x.Uri
	}
	return ""
}

func (x *Gateway) GetResourceName() string {
	if x != nil {
		return x.ResourceName
	}
	return ""
}

func (x *Gateway) GetLocalAddress() string {
	if x != nil {
		return x.LocalAddress
	}
	return ""
}

func (x *Gateway) GetLocalPort() string {
	if x != nil {
		return x.LocalPort
	}
	return ""
}

func (x *Gateway) GetProtocol() string {
	if x != nil {
		return x.Protocol
	}
	return ""
}

func (x *Gateway) GetHostId() string {
	if x != nil {
		return x.HostId
	}
	return ""
}

func (x *Gateway) GetClusterId() string {
	if x != nil {
		return x.ClusterId
	}
	return ""
}

func (x *Gateway) GetStatus() Gateway_GatewayStatus {
	if x != nil {
		return x.Status
	}
	return Gateway_CONNECTED
}

var File_v1_gateway_proto protoreflect.FileDescriptor

var file_v1_gateway_proto_rawDesc = []byte{
	0x0a, 0x10, 0x76, 0x31, 0x2f, 0x67, 0x61, 0x74, 0x65, 0x77, 0x61, 0x79, 0x2e, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x12, 0x14, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x2e, 0x74, 0x65, 0x72,
	0x6d, 0x69, 0x6e, 0x61, 0x6c, 0x2e, 0x76, 0x31, 0x22, 0xcf, 0x02, 0x0a, 0x07, 0x47, 0x61, 0x74,
	0x65, 0x77, 0x61, 0x79, 0x12, 0x10, 0x0a, 0x03, 0x75, 0x72, 0x69, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x03, 0x75, 0x72, 0x69, 0x12, 0x23, 0x0a, 0x0d, 0x72, 0x65, 0x73, 0x6f, 0x75, 0x72,
	0x63, 0x65, 0x5f, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0c, 0x72,
	0x65, 0x73, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x4e, 0x61, 0x6d, 0x65, 0x12, 0x23, 0x0a, 0x0d, 0x6c,
	0x6f, 0x63, 0x61, 0x6c, 0x5f, 0x61, 0x64, 0x64, 0x72, 0x65, 0x73, 0x73, 0x18, 0x03, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x0c, 0x6c, 0x6f, 0x63, 0x61, 0x6c, 0x41, 0x64, 0x64, 0x72, 0x65, 0x73, 0x73,
	0x12, 0x1d, 0x0a, 0x0a, 0x6c, 0x6f, 0x63, 0x61, 0x6c, 0x5f, 0x70, 0x6f, 0x72, 0x74, 0x18, 0x04,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x09, 0x6c, 0x6f, 0x63, 0x61, 0x6c, 0x50, 0x6f, 0x72, 0x74, 0x12,
	0x1a, 0x0a, 0x08, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x63, 0x6f, 0x6c, 0x18, 0x05, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x08, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x63, 0x6f, 0x6c, 0x12, 0x17, 0x0a, 0x07, 0x68,
	0x6f, 0x73, 0x74, 0x5f, 0x69, 0x64, 0x18, 0x06, 0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x68, 0x6f,
	0x73, 0x74, 0x49, 0x64, 0x12, 0x1d, 0x0a, 0x0a, 0x63, 0x6c, 0x75, 0x73, 0x74, 0x65, 0x72, 0x5f,
	0x69, 0x64, 0x18, 0x07, 0x20, 0x01, 0x28, 0x09, 0x52, 0x09, 0x63, 0x6c, 0x75, 0x73, 0x74, 0x65,
	0x72, 0x49, 0x64, 0x12, 0x43, 0x0a, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x18, 0x08, 0x20,
	0x01, 0x28, 0x0e, 0x32, 0x2b, 0x2e, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x2e, 0x74,
	0x65, 0x72, 0x6d, 0x69, 0x6e, 0x61, 0x6c, 0x2e, 0x76, 0x31, 0x2e, 0x47, 0x61, 0x74, 0x65, 0x77,
	0x61, 0x79, 0x2e, 0x47, 0x61, 0x74, 0x65, 0x77, 0x61, 0x79, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73,
	0x52, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x22, 0x30, 0x0a, 0x0d, 0x47, 0x61, 0x74, 0x65,
	0x77, 0x61, 0x79, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x12, 0x0d, 0x0a, 0x09, 0x43, 0x4f, 0x4e,
	0x4e, 0x45, 0x43, 0x54, 0x45, 0x44, 0x10, 0x00, 0x12, 0x10, 0x0a, 0x0c, 0x44, 0x49, 0x53, 0x43,
	0x4f, 0x4e, 0x4e, 0x45, 0x43, 0x54, 0x45, 0x44, 0x10, 0x01, 0x42, 0x33, 0x5a, 0x31, 0x67, 0x69,
	0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x67, 0x72, 0x61, 0x76, 0x69, 0x74, 0x61,
	0x74, 0x69, 0x6f, 0x6e, 0x61, 0x6c, 0x2f, 0x74, 0x65, 0x6c, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x2f,
	0x6c, 0x69, 0x62, 0x2f, 0x74, 0x65, 0x6c, 0x65, 0x74, 0x65, 0x72, 0x6d, 0x2f, 0x76, 0x31, 0x62,
	0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_v1_gateway_proto_rawDescOnce sync.Once
	file_v1_gateway_proto_rawDescData = file_v1_gateway_proto_rawDesc
)

func file_v1_gateway_proto_rawDescGZIP() []byte {
	file_v1_gateway_proto_rawDescOnce.Do(func() {
		file_v1_gateway_proto_rawDescData = protoimpl.X.CompressGZIP(file_v1_gateway_proto_rawDescData)
	})
	return file_v1_gateway_proto_rawDescData
}

var file_v1_gateway_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_v1_gateway_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_v1_gateway_proto_goTypes = []interface{}{
	(Gateway_GatewayStatus)(0), // 0: teleport.terminal.v1.Gateway.GatewayStatus
	(*Gateway)(nil),            // 1: teleport.terminal.v1.Gateway
}
var file_v1_gateway_proto_depIdxs = []int32{
	0, // 0: teleport.terminal.v1.Gateway.status:type_name -> teleport.terminal.v1.Gateway.GatewayStatus
	1, // [1:1] is the sub-list for method output_type
	1, // [1:1] is the sub-list for method input_type
	1, // [1:1] is the sub-list for extension type_name
	1, // [1:1] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_v1_gateway_proto_init() }
func file_v1_gateway_proto_init() {
	if File_v1_gateway_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_v1_gateway_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*Gateway); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_v1_gateway_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_v1_gateway_proto_goTypes,
		DependencyIndexes: file_v1_gateway_proto_depIdxs,
		EnumInfos:         file_v1_gateway_proto_enumTypes,
		MessageInfos:      file_v1_gateway_proto_msgTypes,
	}.Build()
	File_v1_gateway_proto = out.File
	file_v1_gateway_proto_rawDesc = nil
	file_v1_gateway_proto_goTypes = nil
	file_v1_gateway_proto_depIdxs = nil
}
