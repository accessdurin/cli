//go:build linux

package credentials

// Linux: the freedesktop Secret Service over the D-Bus session bus, mirroring
// dbus-secret-service-keyring-store 1.0.1 (as pinned by the TypeScript CLI's
// { linux: { store: 'secret-service' } }) on dbus-secret-service 4.1.0:
//
//   - Lookup: Service.SearchItems over all collections with exactly the
//     attributes {service, username}; locked results are unlocked with
//     Service.Unlock (prompting if the daemon asks); zero matches is a missing
//     entry, more than one is an error (keyring's Ambiguous).
//     (service.rs find_matching_items L31-51, cred.rs get_unique_item L83-110)
//   - Write: SetSecret on the single match; otherwise CreateItem in the
//     collection behind the "default" alias (unlocked first if locked), with
//     label "keyring:<key>@<service>", attributes {service, username},
//     replace = true. (cred.rs set_secret L115-126, Specifier::new L45-47,
//     service.rs create_item L53-79, util::get_collection L207-221)
//   - Secrets carry content type "application/octet-stream" and are the UTF-8
//     bytes of the value. (service.rs L75, L114; keyring-core api.rs L25-27)
//   - Reads decode the secret as strict UTF-8. (keyring-core error.rs L128)
//   - Delete: Item.Delete on the single match; zero matches is a missing
//     entry. (cred.rs delete_credential L155-158)
//   - The session is always encrypted with dh-ietf1024-sha256-aes128-cbc-pkcs7
//     (service.rs L27, dbus-secret-service session.rs crypto module).
//   - Prompts are shown and waited for without a timeout, as
//     dbus-secret-service does (prompt.rs: one-year limit).

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"os"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

var platform backend = secretService{}

const (
	ssDest          = "org.freedesktop.secrets"
	ssPath          = dbus.ObjectPath("/org/freedesktop/secrets")
	ssService       = "org.freedesktop.Secret.Service"
	ssCollection    = "org.freedesktop.Secret.Collection"
	ssItem          = "org.freedesktop.Secret.Item"
	ssPrompt        = "org.freedesktop.Secret.Prompt"
	ssAlgorithmDH   = "dh-ietf1024-sha256-aes128-cbc-pkcs7"
	ssContentType   = "application/octet-stream"
	ssDefaultAlias  = "default"
	ssNoObject      = dbus.ObjectPath("/")
	ssLabelProperty = "org.freedesktop.Secret.Item.Label"
	ssAttrsProperty = "org.freedesktop.Secret.Item.Attributes"
)

type secretService struct{}

func (secretService) get(service, key string) (string, bool, error) {
	s, err := openSession()
	if err != nil {
		return "", false, err
	}
	defer s.close()
	item, found, err := s.uniqueItem(service, key)
	if err != nil || !found {
		return "", false, err
	}
	var secret ssSecret
	if err := s.object(item).Call(ssItem+".GetSecret", 0, s.path).Store(&secret); err != nil {
		return "", false, err
	}
	plain, err := s.decrypt(secret)
	if err != nil {
		return "", false, err
	}
	defer clear(plain)
	if !utf8.Valid(plain) {
		return "", false, errors.New("secret service: stored secret is not UTF-8")
	}
	return string(plain), true, nil
}

func (secretService) set(service, key, value string) error {
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.close()
	plain := []byte(value)
	secret, err := s.encrypt(plain)
	clear(plain)
	if err != nil {
		return err
	}
	item, found, err := s.uniqueItem(service, key)
	if err != nil {
		return err
	}
	if found {
		return s.object(item).Call(ssItem+".SetSecret", 0, secret).Err
	}
	collection, err := s.defaultCollection()
	if err != nil {
		return err
	}
	properties := map[string]dbus.Variant{
		ssLabelProperty: dbus.MakeVariant("keyring:" + key + "@" + service),
		ssAttrsProperty: dbus.MakeVariant(searchAttributes(service, key)),
	}
	var created, prompt dbus.ObjectPath
	if err := s.object(collection).Call(ssCollection+".CreateItem", 0, properties, secret, true).Store(&created, &prompt); err != nil {
		return err
	}
	if created == ssNoObject {
		return s.prompt(prompt)
	}
	return nil
}

func (secretService) remove(service, key string) error {
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.close()
	item, found, err := s.uniqueItem(service, key)
	if err != nil || !found {
		return err
	}
	var prompt dbus.ObjectPath
	if err := s.object(item).Call(ssItem+".Delete", 0).Store(&prompt); err != nil {
		return err
	}
	return s.prompt(prompt)
}

func searchAttributes(service, key string) map[string]string {
	return map[string]string{"service": service, "username": key}
}

// ssSecret is the Secret Service (oayays) secret struct.
type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type ssSession struct {
	conn *dbus.Conn
	path dbus.ObjectPath
	key  []byte // AES-128 key from the DH exchange
}

// openSession connects to the session bus (never launching one) and opens an
// encrypted Secret Service session.
func openSession() (*ssSession, error) {
	address, err := sessionBusAddress()
	if err != nil {
		return nil, err
	}
	conn, err := dbus.Dial(address)
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	s := &ssSession{conn: conn}
	if err := s.negotiate(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *ssSession) close() {
	_ = s.conn.Close()
}

func (s *ssSession) object(path dbus.ObjectPath) dbus.BusObject {
	return s.conn.Object(ssDest, path)
}

// sessionBusAddress returns DBUS_SESSION_BUS_ADDRESS. Like the vendored
// libdbus inside @napi-rs/keyring (built without X11 autolaunch), it does not
// fall back to $XDG_RUNTIME_DIR/bus or start dbus-launch: without the
// variable the store is unavailable, exactly as in the TypeScript CLI.
func sessionBusAddress() (string, error) {
	if address := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); address != "" && address != "autolaunch:" {
		return address, nil
	}
	return "", errors.New("secret service: DBUS_SESSION_BUS_ADDRESS is not set")
}

// uniqueItem finds the single item matching {service, username}, unlocking
// it if needed.
func (s *ssSession) uniqueItem(service, key string) (dbus.ObjectPath, bool, error) {
	var unlocked, locked []dbus.ObjectPath
	if err := s.object(ssPath).Call(ssService+".SearchItems", 0, searchAttributes(service, key)).Store(&unlocked, &locked); err != nil {
		return "", false, err
	}
	if len(locked) > 0 {
		if err := s.unlock(locked); err != nil {
			return "", false, err
		}
	}
	items := append(unlocked, locked...)
	switch len(items) {
	case 0:
		return "", false, nil
	case 1:
		return items[0], true, nil
	default:
		return "", false, fmt.Errorf("secret service: %d items match the entry", len(items))
	}
}

func (s *ssSession) unlock(objects []dbus.ObjectPath) error {
	var done []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := s.object(ssPath).Call(ssService+".Unlock", 0, objects).Store(&done, &prompt); err != nil {
		return err
	}
	return s.prompt(prompt)
}

// defaultCollection returns the collection behind the "default" alias,
// unlocked.
func (s *ssSession) defaultCollection() (dbus.ObjectPath, error) {
	var collection dbus.ObjectPath
	if err := s.object(ssPath).Call(ssService+".ReadAlias", 0, ssDefaultAlias).Store(&collection); err != nil {
		return "", err
	}
	if collection == ssNoObject {
		return "", errors.New("secret service: no default collection")
	}
	lockedProp, err := s.object(collection).GetProperty(ssCollection + ".Locked")
	if err != nil {
		return "", err
	}
	locked, ok := lockedProp.Value().(bool)
	if !ok {
		return "", errors.New("secret service: unexpected Locked property")
	}
	if locked {
		if err := s.unlock([]dbus.ObjectPath{collection}); err != nil {
			return "", err
		}
	}
	return collection, nil
}

// prompt runs a Secret Service prompt, if one was returned, and waits for the
// user's answer. A dismissed prompt is an error.
func (s *ssSession) prompt(path dbus.ObjectPath) error {
	if path == ssNoObject || path == "" {
		return nil
	}
	signals := make(chan *dbus.Signal, 8)
	s.conn.Signal(signals)
	defer s.conn.RemoveSignal(signals)
	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface(ssPrompt),
		dbus.WithMatchMember("Completed"),
	}
	if err := s.conn.AddMatchSignal(match...); err != nil {
		return err
	}
	defer func() { _ = s.conn.RemoveMatchSignal(match...) }()
	if err := s.object(path).Call(ssPrompt+".Prompt", 0, "").Err; err != nil {
		return err
	}
	for signal := range signals {
		if signal.Path != path || signal.Name != ssPrompt+".Completed" {
			continue
		}
		if len(signal.Body) == 0 {
			return errors.New("secret service: malformed prompt result")
		}
		if dismissed, ok := signal.Body[0].(bool); !ok || dismissed {
			return errors.New("secret service: prompt dismissed")
		}
		return nil
	}
	return errors.New("secret service: connection closed during prompt")
}

// Second Oakley group (RFC 2409, 1024-bit MODP), generator 2, as used by the
// Secret Service dh-ietf1024-sha256-aes128-cbc-pkcs7 algorithm.
var (
	dhPrime, _  = new(big.Int).SetString("FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7EDEE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF", 16)
	dhGenerator = big.NewInt(2)
)

// negotiate opens the session with a Diffie-Hellman exchange and derives the
// AES key: HKDF-SHA256 over the shared secret left-padded to 128 bytes, no
// salt, empty info, 16 bytes of output.
func (s *ssSession) negotiate() error {
	privateBytes := make([]byte, 128)
	if _, err := rand.Read(privateBytes); err != nil {
		return err
	}
	private := new(big.Int).SetBytes(privateBytes)
	public := new(big.Int).Exp(dhGenerator, private, dhPrime)
	var output dbus.Variant
	if err := s.object(ssPath).Call(ssService+".OpenSession", 0, ssAlgorithmDH, dbus.MakeVariant(public.Bytes())).Store(&output, &s.path); err != nil {
		return err
	}
	serverPublicBytes, ok := output.Value().([]byte)
	if !ok {
		return errors.New("secret service: unexpected session output")
	}
	serverPublic := new(big.Int).SetBytes(serverPublicBytes)
	if serverPublic.Cmp(big.NewInt(1)) <= 0 || serverPublic.Cmp(new(big.Int).Sub(dhPrime, big.NewInt(1))) >= 0 {
		return errors.New("secret service: invalid server public key")
	}
	shared := new(big.Int).Exp(serverPublic, private, dhPrime).FillBytes(make([]byte, 128))
	key, err := hkdf.Key(sha256.New, shared, nil, "", 16)
	if err != nil {
		return err
	}
	s.key = key
	return nil
}

func (s *ssSession) encrypt(plain []byte) (ssSecret, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return ssSecret{}, err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return ssSecret{}, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append(make([]byte, 0, len(plain)+pad), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
	return ssSecret{Session: s.path, Parameters: iv, Value: padded, ContentType: ssContentType}, nil
}

func (s *ssSession) decrypt(secret ssSecret) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	data := secret.Value
	if len(secret.Parameters) != aes.BlockSize || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("secret service: malformed encrypted secret")
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, secret.Parameters).CryptBlocks(plain, data)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("secret service: bad secret padding")
	}
	return plain[:len(plain)-pad], nil
}
