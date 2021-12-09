// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.23.0
// 	protoc        v3.14.0
// source: torrent-store.proto

package torrent_store

import (
	context "context"
	proto "github.com/golang/protobuf/proto"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
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

// The push response message containing info hash of the pushed torrent file
type PushReply struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	InfoHash string `protobuf:"bytes,1,opt,name=infoHash,proto3" json:"infoHash,omitempty"`
}

func (x *PushReply) Reset() {
	*x = PushReply{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PushReply) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PushReply) ProtoMessage() {}

func (x *PushReply) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PushReply.ProtoReflect.Descriptor instead.
func (*PushReply) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{0}
}

func (x *PushReply) GetInfoHash() string {
	if x != nil {
		return x.InfoHash
	}
	return ""
}

// The push request message containing the torrent and expire duration is seconds
type PushRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Torrent []byte `protobuf:"bytes,1,opt,name=torrent,proto3" json:"torrent,omitempty"`
	// Deprecated: Do not use.
	Expire int32 `protobuf:"varint,2,opt,name=expire,proto3" json:"expire,omitempty"`
}

func (x *PushRequest) Reset() {
	*x = PushRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PushRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PushRequest) ProtoMessage() {}

func (x *PushRequest) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PushRequest.ProtoReflect.Descriptor instead.
func (*PushRequest) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{1}
}

func (x *PushRequest) GetTorrent() []byte {
	if x != nil {
		return x.Torrent
	}
	return nil
}

// Deprecated: Do not use.
func (x *PushRequest) GetExpire() int32 {
	if x != nil {
		return x.Expire
	}
	return 0
}

// The pull request message containing the infoHash
type PullRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	InfoHash string `protobuf:"bytes,1,opt,name=infoHash,proto3" json:"infoHash,omitempty"`
}

func (x *PullRequest) Reset() {
	*x = PullRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PullRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PullRequest) ProtoMessage() {}

func (x *PullRequest) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PullRequest.ProtoReflect.Descriptor instead.
func (*PullRequest) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{2}
}

func (x *PullRequest) GetInfoHash() string {
	if x != nil {
		return x.InfoHash
	}
	return ""
}

// The pull response message containing the torrent
type PullReply struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Torrent []byte `protobuf:"bytes,1,opt,name=torrent,proto3" json:"torrent,omitempty"`
}

func (x *PullReply) Reset() {
	*x = PullReply{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PullReply) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PullReply) ProtoMessage() {}

func (x *PullReply) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PullReply.ProtoReflect.Descriptor instead.
func (*PullReply) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{3}
}

func (x *PullReply) GetTorrent() []byte {
	if x != nil {
		return x.Torrent
	}
	return nil
}

// The check request message containing the infoHash
type CheckRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	InfoHash string `protobuf:"bytes,1,opt,name=infoHash,proto3" json:"infoHash,omitempty"`
}

func (x *CheckRequest) Reset() {
	*x = CheckRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CheckRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CheckRequest) ProtoMessage() {}

func (x *CheckRequest) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CheckRequest.ProtoReflect.Descriptor instead.
func (*CheckRequest) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{4}
}

func (x *CheckRequest) GetInfoHash() string {
	if x != nil {
		return x.InfoHash
	}
	return ""
}

// The check response message containing existance flag
type CheckReply struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Exists bool `protobuf:"varint,1,opt,name=exists,proto3" json:"exists,omitempty"`
}

func (x *CheckReply) Reset() {
	*x = CheckReply{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CheckReply) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CheckReply) ProtoMessage() {}

func (x *CheckReply) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CheckReply.ProtoReflect.Descriptor instead.
func (*CheckReply) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{5}
}

func (x *CheckReply) GetExists() bool {
	if x != nil {
		return x.Exists
	}
	return false
}

// The touch response message
type TouchReply struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *TouchReply) Reset() {
	*x = TouchReply{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *TouchReply) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TouchReply) ProtoMessage() {}

func (x *TouchReply) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TouchReply.ProtoReflect.Descriptor instead.
func (*TouchReply) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{6}
}

// The touch request message containing the torrent and expire duration is seconds
type TouchRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	InfoHash string `protobuf:"bytes,1,opt,name=infoHash,proto3" json:"infoHash,omitempty"`
	// Deprecated: Do not use.
	Expire int32 `protobuf:"varint,2,opt,name=expire,proto3" json:"expire,omitempty"`
}

func (x *TouchRequest) Reset() {
	*x = TouchRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_torrent_store_proto_msgTypes[7]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *TouchRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TouchRequest) ProtoMessage() {}

func (x *TouchRequest) ProtoReflect() protoreflect.Message {
	mi := &file_torrent_store_proto_msgTypes[7]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TouchRequest.ProtoReflect.Descriptor instead.
func (*TouchRequest) Descriptor() ([]byte, []int) {
	return file_torrent_store_proto_rawDescGZIP(), []int{7}
}

func (x *TouchRequest) GetInfoHash() string {
	if x != nil {
		return x.InfoHash
	}
	return ""
}

// Deprecated: Do not use.
func (x *TouchRequest) GetExpire() int32 {
	if x != nil {
		return x.Expire
	}
	return 0
}

var File_torrent_store_proto protoreflect.FileDescriptor

var file_torrent_store_proto_rawDesc = []byte{
	0x0a, 0x13, 0x74, 0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74, 0x2d, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x27, 0x0a, 0x09, 0x50, 0x75, 0x73, 0x68, 0x52, 0x65, 0x70,
	0x6c, 0x79, 0x12, 0x1a, 0x0a, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73, 0x68, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73, 0x68, 0x22, 0x43,
	0x0a, 0x0b, 0x50, 0x75, 0x73, 0x68, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x18, 0x0a,
	0x07, 0x74, 0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x07,
	0x74, 0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74, 0x12, 0x1a, 0x0a, 0x06, 0x65, 0x78, 0x70, 0x69, 0x72,
	0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x05, 0x42, 0x02, 0x18, 0x01, 0x52, 0x06, 0x65, 0x78, 0x70,
	0x69, 0x72, 0x65, 0x22, 0x29, 0x0a, 0x0b, 0x50, 0x75, 0x6c, 0x6c, 0x52, 0x65, 0x71, 0x75, 0x65,
	0x73, 0x74, 0x12, 0x1a, 0x0a, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73, 0x68, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73, 0x68, 0x22, 0x25,
	0x0a, 0x09, 0x50, 0x75, 0x6c, 0x6c, 0x52, 0x65, 0x70, 0x6c, 0x79, 0x12, 0x18, 0x0a, 0x07, 0x74,
	0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x07, 0x74, 0x6f,
	0x72, 0x72, 0x65, 0x6e, 0x74, 0x22, 0x2a, 0x0a, 0x0c, 0x43, 0x68, 0x65, 0x63, 0x6b, 0x52, 0x65,
	0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x1a, 0x0a, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73,
	0x68, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73,
	0x68, 0x22, 0x24, 0x0a, 0x0a, 0x43, 0x68, 0x65, 0x63, 0x6b, 0x52, 0x65, 0x70, 0x6c, 0x79, 0x12,
	0x16, 0x0a, 0x06, 0x65, 0x78, 0x69, 0x73, 0x74, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x08, 0x52,
	0x06, 0x65, 0x78, 0x69, 0x73, 0x74, 0x73, 0x22, 0x0c, 0x0a, 0x0a, 0x54, 0x6f, 0x75, 0x63, 0x68,
	0x52, 0x65, 0x70, 0x6c, 0x79, 0x22, 0x46, 0x0a, 0x0c, 0x54, 0x6f, 0x75, 0x63, 0x68, 0x52, 0x65,
	0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x1a, 0x0a, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73,
	0x68, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x69, 0x6e, 0x66, 0x6f, 0x48, 0x61, 0x73,
	0x68, 0x12, 0x1a, 0x0a, 0x06, 0x65, 0x78, 0x70, 0x69, 0x72, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x05, 0x42, 0x02, 0x18, 0x01, 0x52, 0x06, 0x65, 0x78, 0x70, 0x69, 0x72, 0x65, 0x32, 0x7d, 0x0a,
	0x0c, 0x54, 0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74, 0x53, 0x74, 0x6f, 0x72, 0x65, 0x12, 0x22, 0x0a,
	0x04, 0x50, 0x75, 0x73, 0x68, 0x12, 0x0c, 0x2e, 0x50, 0x75, 0x73, 0x68, 0x52, 0x65, 0x71, 0x75,
	0x65, 0x73, 0x74, 0x1a, 0x0a, 0x2e, 0x50, 0x75, 0x73, 0x68, 0x52, 0x65, 0x70, 0x6c, 0x79, 0x22,
	0x00, 0x12, 0x22, 0x0a, 0x04, 0x50, 0x75, 0x6c, 0x6c, 0x12, 0x0c, 0x2e, 0x50, 0x75, 0x6c, 0x6c,
	0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x0a, 0x2e, 0x50, 0x75, 0x6c, 0x6c, 0x52, 0x65,
	0x70, 0x6c, 0x79, 0x22, 0x00, 0x12, 0x25, 0x0a, 0x05, 0x54, 0x6f, 0x75, 0x63, 0x68, 0x12, 0x0d,
	0x2e, 0x54, 0x6f, 0x75, 0x63, 0x68, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x0b, 0x2e,
	0x54, 0x6f, 0x75, 0x63, 0x68, 0x52, 0x65, 0x70, 0x6c, 0x79, 0x22, 0x00, 0x62, 0x06, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_torrent_store_proto_rawDescOnce sync.Once
	file_torrent_store_proto_rawDescData = file_torrent_store_proto_rawDesc
)

func file_torrent_store_proto_rawDescGZIP() []byte {
	file_torrent_store_proto_rawDescOnce.Do(func() {
		file_torrent_store_proto_rawDescData = protoimpl.X.CompressGZIP(file_torrent_store_proto_rawDescData)
	})
	return file_torrent_store_proto_rawDescData
}

var file_torrent_store_proto_msgTypes = make([]protoimpl.MessageInfo, 8)
var file_torrent_store_proto_goTypes = []interface{}{
	(*PushReply)(nil),    // 0: PushReply
	(*PushRequest)(nil),  // 1: PushRequest
	(*PullRequest)(nil),  // 2: PullRequest
	(*PullReply)(nil),    // 3: PullReply
	(*CheckRequest)(nil), // 4: CheckRequest
	(*CheckReply)(nil),   // 5: CheckReply
	(*TouchReply)(nil),   // 6: TouchReply
	(*TouchRequest)(nil), // 7: TouchRequest
}
var file_torrent_store_proto_depIdxs = []int32{
	1, // 0: TorrentStore.Push:input_type -> PushRequest
	2, // 1: TorrentStore.Pull:input_type -> PullRequest
	7, // 2: TorrentStore.Touch:input_type -> TouchRequest
	0, // 3: TorrentStore.Push:output_type -> PushReply
	3, // 4: TorrentStore.Pull:output_type -> PullReply
	6, // 5: TorrentStore.Touch:output_type -> TouchReply
	3, // [3:6] is the sub-list for method output_type
	0, // [0:3] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_torrent_store_proto_init() }
func file_torrent_store_proto_init() {
	if File_torrent_store_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_torrent_store_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PushReply); i {
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
		file_torrent_store_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PushRequest); i {
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
		file_torrent_store_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PullRequest); i {
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
		file_torrent_store_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PullReply); i {
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
		file_torrent_store_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CheckRequest); i {
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
		file_torrent_store_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CheckReply); i {
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
		file_torrent_store_proto_msgTypes[6].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*TouchReply); i {
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
		file_torrent_store_proto_msgTypes[7].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*TouchRequest); i {
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
			RawDescriptor: file_torrent_store_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   8,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_torrent_store_proto_goTypes,
		DependencyIndexes: file_torrent_store_proto_depIdxs,
		MessageInfos:      file_torrent_store_proto_msgTypes,
	}.Build()
	File_torrent_store_proto = out.File
	file_torrent_store_proto_rawDesc = nil
	file_torrent_store_proto_goTypes = nil
	file_torrent_store_proto_depIdxs = nil
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConnInterface

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion6

// TorrentStoreClient is the client API for TorrentStore service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type TorrentStoreClient interface {
	// Pushes torrent to the store
	Push(ctx context.Context, in *PushRequest, opts ...grpc.CallOption) (*PushReply, error)
	// Pulls torrent from the store
	Pull(ctx context.Context, in *PullRequest, opts ...grpc.CallOption) (*PullReply, error)
	// Touch torrent in the store
	Touch(ctx context.Context, in *TouchRequest, opts ...grpc.CallOption) (*TouchReply, error)
}

type torrentStoreClient struct {
	cc grpc.ClientConnInterface
}

func NewTorrentStoreClient(cc grpc.ClientConnInterface) TorrentStoreClient {
	return &torrentStoreClient{cc}
}

func (c *torrentStoreClient) Push(ctx context.Context, in *PushRequest, opts ...grpc.CallOption) (*PushReply, error) {
	out := new(PushReply)
	err := c.cc.Invoke(ctx, "/TorrentStore/Push", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *torrentStoreClient) Pull(ctx context.Context, in *PullRequest, opts ...grpc.CallOption) (*PullReply, error) {
	out := new(PullReply)
	err := c.cc.Invoke(ctx, "/TorrentStore/Pull", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *torrentStoreClient) Touch(ctx context.Context, in *TouchRequest, opts ...grpc.CallOption) (*TouchReply, error) {
	out := new(TouchReply)
	err := c.cc.Invoke(ctx, "/TorrentStore/Touch", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TorrentStoreServer is the server API for TorrentStore service.
type TorrentStoreServer interface {
	// Pushes torrent to the store
	Push(context.Context, *PushRequest) (*PushReply, error)
	// Pulls torrent from the store
	Pull(context.Context, *PullRequest) (*PullReply, error)
	// Touch torrent in the store
	Touch(context.Context, *TouchRequest) (*TouchReply, error)
}

// UnimplementedTorrentStoreServer can be embedded to have forward compatible implementations.
type UnimplementedTorrentStoreServer struct {
}

func (*UnimplementedTorrentStoreServer) Push(context.Context, *PushRequest) (*PushReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Push not implemented")
}
func (*UnimplementedTorrentStoreServer) Pull(context.Context, *PullRequest) (*PullReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Pull not implemented")
}
func (*UnimplementedTorrentStoreServer) Touch(context.Context, *TouchRequest) (*TouchReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Touch not implemented")
}

func RegisterTorrentStoreServer(s *grpc.Server, srv TorrentStoreServer) {
	s.RegisterService(&_TorrentStore_serviceDesc, srv)
}

func _TorrentStore_Push_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(PushRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(TorrentStoreServer).Push(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/TorrentStore/Push",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(TorrentStoreServer).Push(ctx, req.(*PushRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _TorrentStore_Pull_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(PullRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(TorrentStoreServer).Pull(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/TorrentStore/Pull",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(TorrentStoreServer).Pull(ctx, req.(*PullRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _TorrentStore_Touch_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(TouchRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(TorrentStoreServer).Touch(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/TorrentStore/Touch",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(TorrentStoreServer).Touch(ctx, req.(*TouchRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var _TorrentStore_serviceDesc = grpc.ServiceDesc{
	ServiceName: "TorrentStore",
	HandlerType: (*TorrentStoreServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Push",
			Handler:    _TorrentStore_Push_Handler,
		},
		{
			MethodName: "Pull",
			Handler:    _TorrentStore_Pull_Handler,
		},
		{
			MethodName: "Touch",
			Handler:    _TorrentStore_Touch_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "torrent-store.proto",
}
