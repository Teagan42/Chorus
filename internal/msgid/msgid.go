package msgid

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/teaganglenn/chorus/internal/pb"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ESPHome assigns each API message a wire id via the `id` MessageOption, so the
// mapping is derived from the descriptors instead of a hand-kept table that
// would drift on every ESPHome release.
var (
	idToName = map[uint32]protoreflect.FullName{}
	nameToID = map[protoreflect.FullName]uint32{}
)

func init() {
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := range msgs.Len() {
			md := msgs.Get(i)
			opts, ok := md.Options().(*descriptorpb.MessageOptions)
			if !ok || opts == nil {
				continue
			}
			if !proto.HasExtension(opts, pb.E_Id) {
				continue
			}
			id, ok := proto.GetExtension(opts, pb.E_Id).(uint32)
			if !ok || id == 0 {
				continue
			}
			idToName[id] = md.FullName()
			nameToID[md.FullName()] = id
		}
		return true
	})
}

// For returns the ESPHome wire id for a message.
func For(m proto.Message) (uint32, error) {
	name := m.ProtoReflect().Descriptor().FullName()
	id, ok := nameToID[name]
	if !ok {
		return 0, fmt.Errorf("no ESPHome message id for %s", name)
	}
	return id, nil
}

// New allocates an empty message of the type carrying the given wire id.
// Unknown ids are reported so callers can skip the frame rather than desync.
func New(id uint32) (proto.Message, error) {
	name, ok := idToName[id]
	if !ok {
		return nil, fmt.Errorf("unknown ESPHome message id %d", id)
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(name)
	if err != nil {
		return nil, fmt.Errorf("message %s (id %d) not in registry: %w", name, id, err)
	}
	return mt.New().Interface(), nil
}

// Known reports how many id-bearing messages were discovered. Guards against
// a generation step that silently produced descriptors without the extension.
func Known() int { return len(idToName) }
