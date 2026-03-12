package bitcoin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// AddressDeriver handles Bitcoin address derivation from Nostr pubkeys
type AddressDeriver struct {
	network *chaincfg.Params
}

// NewAddressDeriver creates a new address deriver
func NewAddressDeriver(testnet bool) *AddressDeriver {
	network := &chaincfg.MainNetParams
	if testnet {
		network = &chaincfg.TestNet3Params
	}

	return &AddressDeriver{
		network: network,
	}
}

// DeriveAddresses derives various Bitcoin address types from a Nostr pubkey
// For P2WPKH, derives BOTH even-y (0x02) and odd-y (0x03) variants since
// Nostr pubkeys are x-only and we don't know which y-coordinate the user's
// wallet is using.
func (ad *AddressDeriver) DeriveAddresses(nostrPubkeyHex string) ([]DerivedAddress, error) {
	// Decode hex pubkey
	pubkeyBytes, err := hex.DecodeString(nostrPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex pubkey: %w", err)
	}

	if len(pubkeyBytes) != 32 {
		return nil, fmt.Errorf("invalid pubkey length: expected 32 bytes, got %d", len(pubkeyBytes))
	}

	var addresses []DerivedAddress

	// 1. P2WPKH (Native SegWit - bc1q...) - derive BOTH y-coordinate variants
	// Since Nostr uses x-only pubkeys, we don't know which y the user's wallet chose
	// Some wallets use "natural" (could be even or odd), some force even-y

	// 1a. Even y-coordinate (0x02 prefix) - "forced even" convention
	pubKeyEven, err := btcec.ParsePubKey(append([]byte{0x02}, pubkeyBytes...))
	if err == nil {
		p2wpkhEven, err := ad.deriveP2WPKH(pubKeyEven)
		if err == nil {
			addresses = append(addresses, DerivedAddress{
				Address: p2wpkhEven,
				Type:    "p2wpkh",
				Network: ad.network.Name,
			})
		}
	}

	// 1b. Odd y-coordinate (0x03 prefix) - "natural" for keys with odd y
	pubKeyOdd, err := btcec.ParsePubKey(append([]byte{0x03}, pubkeyBytes...))
	if err == nil {
		p2wpkhOdd, err := ad.deriveP2WPKH(pubKeyOdd)
		if err == nil {
			addresses = append(addresses, DerivedAddress{
				Address: p2wpkhOdd,
				Type:    "p2wpkh",
				Network: ad.network.Name,
			})
		}
	}

	// 2. P2TR (Taproot - bc1p...) - x-only, no y-coordinate ambiguity
	p2tr, err := ad.deriveP2TR(pubkeyBytes)
	if err == nil {
		addresses = append(addresses, DerivedAddress{
			Address: p2tr,
			Type:    "p2tr",
			Network: ad.network.Name,
		})
	}

	// 3. P2PKH (Legacy - 1...) - derive both variants
	if pubKeyEven != nil {
		p2pkh, err := ad.deriveP2PKH(pubKeyEven)
		if err == nil {
			addresses = append(addresses, DerivedAddress{
				Address: p2pkh,
				Type:    "p2pkh",
				Network: ad.network.Name,
			})
		}
	}
	if pubKeyOdd != nil {
		p2pkhOdd, err := ad.deriveP2PKH(pubKeyOdd)
		if err == nil {
			addresses = append(addresses, DerivedAddress{
				Address: p2pkhOdd,
				Type:    "p2pkh",
				Network: ad.network.Name,
			})
		}
	}

	// 4. P2SH-P2WPKH (Wrapped SegWit - 3...) - derive both variants
	if pubKeyEven != nil {
		p2shP2wpkh, err := ad.deriveP2SH_P2WPKH(pubKeyEven)
		if err == nil {
			addresses = append(addresses, DerivedAddress{
				Address: p2shP2wpkh,
				Type:    "p2sh-p2wpkh",
				Network: ad.network.Name,
			})
		}
	}
	if pubKeyOdd != nil {
		p2shP2wpkhOdd, err := ad.deriveP2SH_P2WPKH(pubKeyOdd)
		if err == nil {
			addresses = append(addresses, DerivedAddress{
				Address: p2shP2wpkhOdd,
				Type:    "p2sh-p2wpkh",
				Network: ad.network.Name,
			})
		}
	}

	if len(addresses) == 0 {
		return nil, fmt.Errorf("failed to derive any addresses")
	}

	return addresses, nil
}

// deriveP2WPKH derives a P2WPKH (native SegWit) address
func (ad *AddressDeriver) deriveP2WPKH(pubKey *btcec.PublicKey) (string, error) {
	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())

	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, ad.network)
	if err != nil {
		return "", err
	}

	return addr.EncodeAddress(), nil
}

// deriveP2TR derives a P2TR (Taproot) address using BIP-86
func (ad *AddressDeriver) deriveP2TR(pubkeyBytes []byte) (string, error) {
	// For BIP-86 (standard Taproot), we need to:
	// 1. Parse the pubkey as a secp256k1 point
	// 2. Tweak it with the taproot commitment

	// Parse as secp256k1 public key (try 0x02 prefix first)
	pubKey, err := btcec.ParsePubKey(append([]byte{0x02}, pubkeyBytes...))
	if err != nil {
		// Try with 0x03 prefix
		pubKey, err = btcec.ParsePubKey(append([]byte{0x03}, pubkeyBytes...))
		if err != nil {
			return "", fmt.Errorf("failed to parse pubkey for taproot: %w", err)
		}
	}

	// Get the x-only pubkey and tweak it for BIP-86
	// This creates a key-path-only Taproot output
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	addr, err := btcutil.NewAddressTaproot(taprootKey.SerializeCompressed()[1:], ad.network)
	if err != nil {
		return "", err
	}

	return addr.EncodeAddress(), nil
}

// deriveP2PKH derives a P2PKH (legacy) address
func (ad *AddressDeriver) deriveP2PKH(pubKey *btcec.PublicKey) (string, error) {
	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())

	addr, err := btcutil.NewAddressPubKeyHash(pubKeyHash, ad.network)
	if err != nil {
		return "", err
	}

	return addr.EncodeAddress(), nil
}

// deriveP2SH_P2WPKH derives a P2SH-P2WPKH (wrapped SegWit) address
func (ad *AddressDeriver) deriveP2SH_P2WPKH(pubKey *btcec.PublicKey) (string, error) {
	pubKeyHash := btcutil.Hash160(pubKey.SerializeCompressed())

	// Create witness program
	witnessProgram, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, ad.network)
	if err != nil {
		return "", err
	}

	// Get the script
	script, err := txscript.PayToAddrScript(witnessProgram)
	if err != nil {
		return "", err
	}

	// Hash the script for P2SH
	scriptHash := btcutil.Hash160(script)

	addr, err := btcutil.NewAddressScriptHash(scriptHash, ad.network)
	if err != nil {
		return "", err
	}

	return addr.EncodeAddress(), nil
}

// DerivedAddress represents a derived Bitcoin address
type DerivedAddress struct {
	Address string `json:"address"`
	Type    string `json:"type"`    // p2wpkh, p2tr, p2pkh, p2sh-p2wpkh
	Network string `json:"network"` // mainnet, testnet3, regtest
}

// VerifyAddressOwnership verifies that a Bitcoin address belongs to a Nostr pubkey
func (ad *AddressDeriver) VerifyAddressOwnership(bitcoinAddr string, nostrPubkeyHex string) (bool, error) {
	// Derive all possible addresses
	derivedAddrs, err := ad.DeriveAddresses(nostrPubkeyHex)
	if err != nil {
		return false, fmt.Errorf("failed to derive addresses: %w", err)
	}

	// Check if the Bitcoin address matches any derived address
	for _, derived := range derivedAddrs {
		if derived.Address == bitcoinAddr {
			return true, nil
		}
	}

	return false, nil
}

// GetAddressType determines the type of a Bitcoin address
func (ad *AddressDeriver) GetAddressType(address string) (string, error) {
	addr, err := btcutil.DecodeAddress(address, ad.network)
	if err != nil {
		return "", fmt.Errorf("invalid address: %w", err)
	}

	switch addr.(type) {
	case *btcutil.AddressPubKeyHash:
		return "p2pkh", nil
	case *btcutil.AddressScriptHash:
		return "p2sh", nil
	case *btcutil.AddressWitnessPubKeyHash:
		return "p2wpkh", nil
	case *btcutil.AddressWitnessScriptHash:
		return "p2wsh", nil
	case *btcutil.AddressTaproot:
		return "p2tr", nil
	default:
		return "unknown", nil
	}
}

// ValidateAddress validates a Bitcoin address format
func (ad *AddressDeriver) ValidateAddress(address string) error {
	_, err := btcutil.DecodeAddress(address, ad.network)
	if err != nil {
		return fmt.Errorf("invalid Bitcoin address: %w", err)
	}
	return nil
}

// DeriveNostrPubkeyFromBitcoinPrivkey derives a Nostr pubkey from a Bitcoin private key
// This is useful for testing and compatibility
func DeriveNostrPubkeyFromBitcoinPrivkey(privkeyWIF string, network *chaincfg.Params) (string, error) {
	// Decode WIF private key
	wif, err := btcutil.DecodeWIF(privkeyWIF)
	if err != nil {
		return "", fmt.Errorf("failed to decode WIF: %w", err)
	}

	// Get public key
	pubKey := wif.PrivKey.PubKey()

	// For Nostr, we use the x-coordinate of the public key (32 bytes)
	// This is the same as Taproot x-only pubkey
	xCoord := pubKey.X().Bytes()

	// Ensure it's 32 bytes
	if len(xCoord) < 32 {
		// Pad with zeros if necessary
		padded := make([]byte, 32)
		copy(padded[32-len(xCoord):], xCoord)
		xCoord = padded
	}

	return hex.EncodeToString(xCoord), nil
}

// CalculateTxID calculates the transaction ID from raw transaction bytes
func CalculateTxID(txBytes []byte) string {
	hash := sha256.Sum256(txBytes)
	hash = sha256.Sum256(hash[:])

	// Reverse the hash for display (Bitcoin convention)
	for i := 0; i < len(hash)/2; i++ {
		hash[i], hash[len(hash)-1-i] = hash[len(hash)-1-i], hash[i]
	}

	return hex.EncodeToString(hash[:])
}
