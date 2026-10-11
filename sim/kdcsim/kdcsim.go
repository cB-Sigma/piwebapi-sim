// Package kdcsim is an in-process Kerberos V5 key distribution centre
// for tests, built from RFC 4120 (sections 3.1 and 3.3, the AS and TGS
// exchanges, and 7.2 for the TCP and UDP framing) and RFC 3962 (AES
// encryption types): one realm with users (password keys, salted with
// realm and name), services (HTTP/host) and krbtgt. The AS exchange
// requires pre-authentication: a request without PA-ENC-TIMESTAMP is
// answered KDC_ERR_PREAUTH_REQUIRED with ETYPE-INFO2, a timestamp the
// user's key cannot decrypt KDC_ERR_PREAUTH_FAILED, an unknown user
// KDC_ERR_C_PRINCIPAL_UNKNOWN. The TGS exchange decrypts the ticket-
// granting ticket in PA-TGS-REQ with the krbtgt key, checks the
// authenticator with its session key and issues a service ticket
// encrypted with the service's key; an unknown service is
// KDC_ERR_S_PRINCIPAL_UNKNOWN.
//
// The message types and cryptography come from github.com/jcmturner/
// gokrb5 (its message encoders and RFC 3961 profiles); the exchange logic
// is this package's own, so a client is tested against a KDC's decisions,
// not against its own code. Services verify the tickets with gokrb5's
// SPNEGO acceptor and the keytab ServiceKeytab returns.
package kdcsim

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/jcmturner/gofork/encoding/asn1"
	"github.com/jcmturner/gokrb5/v8/crypto"
	"github.com/jcmturner/gokrb5/v8/iana/errorcode"
	"github.com/jcmturner/gokrb5/v8/iana/etypeID"
	"github.com/jcmturner/gokrb5/v8/iana/flags"
	"github.com/jcmturner/gokrb5/v8/iana/keyusage"
	"github.com/jcmturner/gokrb5/v8/iana/msgtype"
	"github.com/jcmturner/gokrb5/v8/iana/nametype"
	"github.com/jcmturner/gokrb5/v8/iana/patype"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/types"
)

const etype = etypeID.AES256_CTS_HMAC_SHA1_96

// KDC is the key distribution centre.
type KDC struct {
	Realm string
	// Addr is the TCP and UDP host:port.
	Addr string

	tcp net.Listener
	udp net.PacketConn

	mu       sync.Mutex
	keys     *keytab.Keytab // krbtgt, users and services
	users    map[string]bool
	services map[string]string // principal -> password
	// Log are the exchanges, in order ("AS sigma: preauth required",
	// "TGS sigma -> HTTP/host").
	Log []string
	wg  sync.WaitGroup
}

// Start starts a KDC for realm on 127.0.0.1.
func Start(realm string) (*KDC, error) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		return nil, err
	}
	k := &KDC{Realm: realm, Addr: tcp.Addr().String(), tcp: tcp, udp: udp, keys: keytab.New(),
		users: map[string]bool{}, services: map[string]string{}}
	if err := k.keys.AddEntry("krbtgt/"+realm, realm, "krbtgt-"+realm+"-"+time.Now().String(), time.Now(), 1, etype); err != nil {
		return nil, err
	}
	k.wg.Add(2)
	go k.serveTCP()
	go k.serveUDP()
	return k, nil
}

// Close stops the KDC.
func (k *KDC) Close() {
	k.tcp.Close()
	k.udp.Close()
	k.wg.Wait()
}

// AddUser adds a user principal with a password.
func (k *KDC) AddUser(name, password string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.users[name] = true
	return k.keys.AddEntry(name, k.Realm, password, time.Now(), 1, etype)
}

// UserKeytab is a keytab holding the user's key, as ktutil or ktpass
// would export it.
func (k *KDC) UserKeytab(name, password string) ([]byte, error) {
	kt := keytab.New()
	if err := kt.AddEntry(name, k.Realm, password, time.Now(), 1, etype); err != nil {
		return nil, err
	}
	return kt.Marshal()
}

// AddService adds a service principal (HTTP/pi.plant.example) and
// returns its keytab, for the service's acceptor.
func (k *KDC) AddService(spn, password string) (*keytab.Keytab, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.services[spn] = password
	if err := k.keys.AddEntry(spn, k.Realm, password, time.Now(), 1, etype); err != nil {
		return nil, err
	}
	kt := keytab.New()
	if err := kt.AddEntry(spn, k.Realm, password, time.Now(), 1, etype); err != nil {
		return nil, err
	}
	return kt, nil
}

// Krb5Conf is a krb5.conf for the realm with this KDC.
func (k *KDC) Krb5Conf() string {
	return fmt.Sprintf(`[libdefaults]
  default_realm = %[1]s
  dns_lookup_kdc = false
  dns_lookup_realm = false
  udp_preference_limit = 1
  default_tkt_enctypes = aes256-cts-hmac-sha1-96
  default_tgs_enctypes = aes256-cts-hmac-sha1-96
  permitted_enctypes = aes256-cts-hmac-sha1-96

[realms]
  %[1]s = {
    kdc = %[2]s
  }
`, k.Realm, k.Addr)
}

func (k *KDC) log(format string, a ...any) {
	k.mu.Lock()
	k.Log = append(k.Log, fmt.Sprintf(format, a...))
	k.mu.Unlock()
}

// Exchanges returns the log.
func (k *KDC) Exchanges() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.Log...)
}

func (k *KDC) serveTCP() {
	defer k.wg.Done()
	for {
		c, err := k.tcp.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			var n uint32
			if err := binary.Read(c, binary.BigEndian, &n); err != nil || n > 1<<20 {
				return
			}
			req := make([]byte, n)
			if _, err := io.ReadFull(c, req); err != nil {
				return
			}
			rep := k.handle(req)
			_ = binary.Write(c, binary.BigEndian, uint32(len(rep)))
			_, _ = c.Write(rep)
		}()
	}
}

func (k *KDC) serveUDP() {
	defer k.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, from, err := k.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		rep := k.handle(append([]byte(nil), buf[:n]...))
		_, _ = k.udp.WriteTo(rep, from)
	}
}

// handle answers one KDC request.
func (k *KDC) handle(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	// The application tag tells AS-REQ (10) from TGS-REQ (12).
	switch b[0] & 0x1f {
	case 10:
		var req messages.ASReq
		if err := req.Unmarshal(b); err != nil {
			return k.krbErr(types.PrincipalName{}, errorcode.KRB_ERR_GENERIC, "cannot read the AS-REQ", nil)
		}
		return k.as(req)
	case 12:
		var req messages.TGSReq
		if err := req.Unmarshal(b); err != nil {
			return k.krbErr(types.PrincipalName{}, errorcode.KRB_ERR_GENERIC, "cannot read the TGS-REQ", nil)
		}
		return k.tgs(req)
	}
	return k.krbErr(types.PrincipalName{}, errorcode.KRB_ERR_GENERIC, "not a KDC request", nil)
}

func (k *KDC) krbErr(sname types.PrincipalName, code int32, text string, edata []byte) []byte {
	e := messages.NewKRBError(sname, k.Realm, code, text)
	e.EData = edata
	b, _ := e.Marshal()
	return b
}

func (k *KDC) key(p types.PrincipalName) (types.EncryptionKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key, _, err := k.keys.GetEncryptionKey(p, k.Realm, 0, etype)
	return key, err
}

func (k *KDC) as(req messages.ASReq) []byte {
	cname := req.ReqBody.CName
	name := cname.PrincipalNameString()
	k.mu.Lock()
	known := k.users[name]
	k.mu.Unlock()
	if !known || req.ReqBody.Realm != k.Realm {
		k.log("AS %s: unknown", name)
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_C_PRINCIPAL_UNKNOWN, "", nil)
	}
	ukey, err := k.key(cname)
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_C_PRINCIPAL_UNKNOWN, "", nil)
	}
	var ts []byte
	for _, pa := range req.PAData {
		if pa.PADataType == patype.PA_ENC_TIMESTAMP {
			ts = pa.PADataValue
		}
	}
	if ts == nil {
		k.log("AS %s: preauth required", name)
		info, _ := asn1.Marshal(types.ETypeInfo2{{EType: etype, Salt: cname.GetSalt(k.Realm)}})
		ed, _ := asn1.Marshal(types.PADataSequence{{PADataType: patype.PA_ETYPE_INFO2, PADataValue: info}})
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_PREAUTH_REQUIRED, "", ed)
	}
	var enc types.EncryptedData
	if err := enc.Unmarshal(ts); err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_PREAUTH_FAILED, "", nil)
	}
	plain, err := crypto.DecryptEncPart(enc, ukey, keyusage.AS_REQ_PA_ENC_TIMESTAMP)
	var pt types.PAEncTSEnc
	if err == nil {
		err = pt.Unmarshal(plain)
	}
	if err != nil || absDur(time.Since(pt.PATimestamp)) > 5*time.Minute {
		k.log("AS %s: preauth failed", name)
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_PREAUTH_FAILED, "", nil)
	}
	tgs := types.NewPrincipalName(nametype.KRB_NT_SRV_INST, "krbtgt/"+k.Realm)
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(10 * time.Hour)
	fl := types.NewKrbFlags()
	types.SetFlag(&fl, flags.Initial)
	types.SetFlag(&fl, flags.PreAuthent)
	k.mu.Lock()
	tkt, sess, err := messages.NewTicket(cname, k.Realm, tgs, k.Realm, fl, k.keys, etype, 1, now, now, end, end)
	k.mu.Unlock()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	part := messages.EncKDCRepPart{Key: sess, LastReqs: []messages.LastReq{{LRType: 0, LRValue: now}}, Nonce: req.ReqBody.Nonce,
		Flags: fl, AuthTime: now, StartTime: now, EndTime: end, RenewTill: end, SRealm: k.Realm, SName: req.ReqBody.SName}
	pb, err := part.Marshal()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	ed, err := crypto.GetEncryptedData(pb, ukey, keyusage.AS_REP_ENCPART, 1)
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	info, _ := asn1.Marshal(types.ETypeInfo2{{EType: etype, Salt: cname.GetSalt(k.Realm)}})
	rep := messages.ASRep{KDCRepFields: messages.KDCRepFields{PVNO: 5, MsgType: msgtype.KRB_AS_REP, CRealm: k.Realm, CName: cname,
		Ticket: tkt, EncPart: ed, PAData: types.PADataSequence{{PADataType: patype.PA_ETYPE_INFO2, PADataValue: info}}}}
	b, err := rep.Marshal()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	k.log("AS %s: ticket-granting ticket", name)
	return b
}

func (k *KDC) tgs(req messages.TGSReq) []byte {
	var apb []byte
	for _, pa := range req.PAData {
		if pa.PADataType == patype.PA_TGS_REQ {
			apb = pa.PADataValue
		}
	}
	var ap messages.APReq
	if apb == nil || ap.Unmarshal(apb) != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_PADATA_TYPE_NOSUPP, "no PA-TGS-REQ", nil)
	}
	k.mu.Lock()
	err := ap.Ticket.DecryptEncPart(k.keys, nil)
	k.mu.Unlock()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_AP_ERR_BAD_INTEGRITY, "the ticket-granting ticket does not decrypt", nil)
	}
	sess := ap.Ticket.DecryptedEncPart.Key
	if err := ap.DecryptAuthenticator(sess); err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_AP_ERR_BAD_INTEGRITY, "the authenticator does not decrypt", nil)
	}
	cname := ap.Ticket.DecryptedEncPart.CName
	if !ap.Authenticator.CName.Equal(cname) {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_AP_ERR_BADMATCH, "", nil)
	}
	spn := req.ReqBody.SName.PrincipalNameString()
	k.mu.Lock()
	_, known := k.services[spn]
	k.mu.Unlock()
	if !known {
		k.log("TGS %s -> %s: unknown service", cname.PrincipalNameString(), spn)
		return k.krbErr(req.ReqBody.SName, errorcode.KDC_ERR_S_PRINCIPAL_UNKNOWN, "", nil)
	}
	now := time.Now().UTC().Truncate(time.Second)
	end := ap.Ticket.DecryptedEncPart.EndTime
	fl := types.NewKrbFlags()
	types.SetFlag(&fl, flags.PreAuthent)
	k.mu.Lock()
	tkt, skey, err := messages.NewTicket(cname, k.Realm, req.ReqBody.SName, k.Realm, fl, k.keys, etype, 1, ap.Ticket.DecryptedEncPart.AuthTime, now, end, end)
	k.mu.Unlock()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	part := messages.EncKDCRepPart{Key: skey, LastReqs: []messages.LastReq{{LRType: 0, LRValue: now}}, Nonce: req.ReqBody.Nonce,
		Flags: fl, AuthTime: ap.Ticket.DecryptedEncPart.AuthTime, StartTime: now, EndTime: end, RenewTill: end, SRealm: k.Realm, SName: req.ReqBody.SName}
	pb, err := part.Marshal()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	ed, err := crypto.GetEncryptedData(pb, sess, keyusage.TGS_REP_ENCPART_SESSION_KEY, 0)
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	rep := messages.TGSRep{KDCRepFields: messages.KDCRepFields{PVNO: 5, MsgType: msgtype.KRB_TGS_REP, CRealm: k.Realm, CName: cname, Ticket: tkt, EncPart: ed}}
	b, err := rep.Marshal()
	if err != nil {
		return k.krbErr(req.ReqBody.SName, errorcode.KRB_ERR_GENERIC, err.Error(), nil)
	}
	k.log("TGS %s -> %s", cname.PrincipalNameString(), spn)
	return b
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
