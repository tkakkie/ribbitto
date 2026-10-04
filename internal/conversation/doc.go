// Package conversation is the conversation module's root (decisions 26 and
// 27): channels, topics and messages, moving here from the layers during
// step 4 of the migration (docs/architecture/modules.md). IDs are kernel.ID;
// the package never imports domain.
//
// Exported API so far: Channel, its name rule, errors and default name;
// Message, ValidateMessageBody, ErrInvalidBody and ErrMessageNotFound; and the
// two event kinds conversation publishes and so owns.
// KindPosted with Posted, EncodePosted, DecodePosted and RoutePosted is
// message.posted, its payload and its routing; KindMessagesMoved with Moved,
// EncodeMoved, DecodeMoved and RouteMoved is messages.moved. conversationpg
// registers both routers with realtime's reader.
package conversation
