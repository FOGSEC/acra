package acra

import (
	"bytes"
	"encoding/binary"
	"fmt"
	. "github.com/cossacklabs/acra/utils"
	"github.com/cossacklabs/themis/gothemis/cell"
	"github.com/cossacklabs/themis/gothemis/keys"
	"github.com/cossacklabs/themis/gothemis/message"
	"io"
	"log"
	"github.com/cossacklabs/acra/keystore"
	"github.com/cossacklabs/acra/zone"
)

type BinaryDecryptor struct {
	current_index    uint8
	is_with_zone     bool
	key_block_buffer []byte
	length_buf       [DATA_LENGTH_SIZE]byte
	buf              []byte
	key_store        keystore.KeyStore
	zone_matcher     *zone.ZoneIdMatcher
	poison_key       []byte
	callback_storage *PoisonCallbackStorage
}

func NewBinaryDecryptor() Decryptor {
	return &BinaryDecryptor{key_block_buffer: make([]byte, KEY_BLOCK_LENGTH)}
}

/* not implemented Decryptor interface */
func (decryptor *BinaryDecryptor) MatchBeginTag(char byte) bool {
	if char == TAG_BEGIN[decryptor.current_index] {
		decryptor.current_index++
		return true
	}
	return false
}
func (decryptor *BinaryDecryptor) IsMatched() bool {
	return len(TAG_BEGIN) == int(decryptor.current_index)
}
func (decryptor *BinaryDecryptor) Reset() {
	decryptor.current_index = 0
}
func (decryptor *BinaryDecryptor) GetMatched() []byte {
	return TAG_BEGIN[:decryptor.current_index]
}
func (decryptor *BinaryDecryptor) ReadSymmetricKey(private_key *keys.PrivateKey, reader io.Reader) ([]byte, []byte, error) {
	n, err := io.ReadFull(reader, decryptor.key_block_buffer[:])
	if err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil, decryptor.key_block_buffer[:n], FAKE_ACRA_STRUCT
		} else {
			return nil, decryptor.key_block_buffer[:n], err
		}
	}
	pubkey := &keys.PublicKey{Value: decryptor.key_block_buffer[:PUBLIC_KEY_LENGTH]}

	smessage := message.New(private_key, pubkey)
	symmetric_key, err := smessage.Unwrap(decryptor.key_block_buffer[PUBLIC_KEY_LENGTH:])
	if err != nil {
		log.Printf("Warning: %v\n", ErrorMessage("can't unwrap scell data", err))
		return nil, decryptor.key_block_buffer[:n], FAKE_ACRA_STRUCT
	}
	return symmetric_key, decryptor.key_block_buffer[:n], nil
}

func (decryptor *BinaryDecryptor) readDataLength(reader io.Reader) (uint64, []byte, error) {
	var length uint64
	len_count, err := io.ReadFull(reader, decryptor.length_buf[:])
	if err != nil {
		log.Printf("Warning: %v\n", ErrorMessage("can't read data length", err))
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return 0, []byte{}, FAKE_ACRA_STRUCT
		} else {
			return 0, []byte{}, err
		}
	}
	if len_count != len(decryptor.length_buf) {
		log.Printf("Warning: incorrect length count, %v!=%v\n", len_count, len(decryptor.length_buf))
		return 0, decryptor.length_buf[:len_count], FAKE_ACRA_STRUCT
	}
	// convert from little endian
	binary.Read(bytes.NewReader(decryptor.length_buf[:]), binary.LittleEndian, &length)
	return length, decryptor.length_buf[:], nil
}

func (decryptor *BinaryDecryptor) checkBuf(buf *[]byte, length int) {
	if buf == nil || len(*buf) < length {
		*buf = make([]byte, length)
	}
}

func (decryptor *BinaryDecryptor) readScellData(length int, reader io.Reader) ([]byte, []byte, error) {
	decryptor.checkBuf(&decryptor.buf, int(length))
	n, err := io.ReadFull(reader, decryptor.buf[:length])
	if err != nil {
		log.Printf("Warning: %v\n", ErrorMessage(fmt.Sprintf("can't read scell data with passed length=%v", length), err))
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil, decryptor.buf[:n], FAKE_ACRA_STRUCT
		} else {
			return nil, decryptor.buf[:n], err
		}
	}
	if n != int(length) {
		log.Printf("Warning: %v\n", ErrorMessage("can't decode hex data", err))
		return nil, decryptor.buf[:n], FAKE_ACRA_STRUCT
	}
	return decryptor.buf[:length], decryptor.buf[:length], nil
}

func (decryptor *BinaryDecryptor) ReadData(symmetric_key, zone_id []byte, reader io.Reader) ([]byte, error) {
	length, raw_length_data, err := decryptor.readDataLength(reader)
	if err != nil {
		return raw_length_data, err
	}
	data, raw_data, err := decryptor.readScellData(int(length), reader)
	if err != nil {
		return append(raw_length_data, raw_data...), err
	}

	scell := cell.New(symmetric_key, cell.CELL_MODE_SEAL)
	decrypted, err := scell.Unprotect(data, nil, zone_id)
	data = nil
	// fill zero symmetric_key
	FillSlice(byte(0), symmetric_key)
	if err != nil {
		return append(raw_length_data, raw_data...), FAKE_ACRA_STRUCT
	}
	return decrypted, nil
}

func (decryptor *BinaryDecryptor) SetKeyStore(store keystore.KeyStore) {
	decryptor.key_store = store
}

func (decryptor *BinaryDecryptor) GetPrivateKey() (*keys.PrivateKey, error) {
	return decryptor.key_store.GetKey(decryptor.GetMatchedZoneId())
}

func (decryptor *BinaryDecryptor) GetPoisonCallbackStorage() *PoisonCallbackStorage {
	return decryptor.callback_storage
}

func (decryptor *BinaryDecryptor) SetPoisonCallbackStorage(storage *PoisonCallbackStorage) {
	decryptor.callback_storage = storage
}

func (decryptor *BinaryDecryptor) SetPoisonKey(key []byte) {
	decryptor.poison_key = key
}

func (decryptor *BinaryDecryptor) GetPoisonKey() []byte {
	return decryptor.poison_key
}

func (decryptor *BinaryDecryptor) SetZoneMatcher(zone_matcher *zone.ZoneIdMatcher) {
	decryptor.zone_matcher = zone_matcher
}

func (decryptor *BinaryDecryptor) GetMatchedZoneId() []byte {
	if decryptor.IsWithZone() {
		return decryptor.zone_matcher.GetZoneId()
	} else {
		return []byte{}
	}
}

func (decryptor *BinaryDecryptor) ResetZoneMatch() {
	decryptor.zone_matcher.Reset()
}

func (decryptor *BinaryDecryptor) IsMatchedZone() bool {
	return decryptor.zone_matcher.IsMatched() && decryptor.key_store.HasKey(decryptor.zone_matcher.GetZoneId())
}

func (decryptor *BinaryDecryptor) MatchZone(b byte) bool {
	return decryptor.zone_matcher.Match(b)
}

func (decryptor *BinaryDecryptor) IsWithZone() bool {
	return decryptor.is_with_zone
}

func (decryptor *BinaryDecryptor) SetWithZone(b bool) {
	decryptor.is_with_zone = b
}

/* end not implemented Decryptor interface */
