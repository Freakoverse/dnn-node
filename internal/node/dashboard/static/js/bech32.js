// bech32.js - Bech32/Bech32m encoding for Bitcoin addresses
// Minimal implementation for deriving Taproot (bc1p) addresses from Nostr pubkeys

(function (global) {
    'use strict';

    const CHARSET = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l';
    const GENERATOR = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];

    function polymod(values) {
        let chk = 1;
        for (let v of values) {
            const top = chk >> 25;
            chk = ((chk & 0x1ffffff) << 5) ^ v;
            for (let i = 0; i < 5; i++) {
                if ((top >> i) & 1) {
                    chk ^= GENERATOR[i];
                }
            }
        }
        return chk;
    }

    function hrpExpand(hrp) {
        const result = [];
        for (let i = 0; i < hrp.length; i++) {
            result.push(hrp.charCodeAt(i) >> 5);
        }
        result.push(0);
        for (let i = 0; i < hrp.length; i++) {
            result.push(hrp.charCodeAt(i) & 31);
        }
        return result;
    }

    function createChecksum(hrp, data, spec) {
        const values = hrpExpand(hrp).concat(data);
        const polymodConst = spec === 'bech32m' ? 0x2bc830a3 : 1;
        const poly = polymod(values.concat([0, 0, 0, 0, 0, 0])) ^ polymodConst;
        const result = [];
        for (let i = 0; i < 6; i++) {
            result.push((poly >> (5 * (5 - i))) & 31);
        }
        return result;
    }

    function convertBits(data, fromBits, toBits, pad) {
        let acc = 0;
        let bits = 0;
        const result = [];
        const maxv = (1 << toBits) - 1;

        for (let value of data) {
            if (value < 0 || (value >> fromBits) !== 0) {
                throw new Error('Invalid value for convertBits');
            }
            acc = (acc << fromBits) | value;
            bits += fromBits;
            while (bits >= toBits) {
                bits -= toBits;
                result.push((acc >> bits) & maxv);
            }
        }

        if (pad) {
            if (bits > 0) {
                result.push((acc << (toBits - bits)) & maxv);
            }
        } else if (bits >= fromBits || ((acc << (toBits - bits)) & maxv)) {
            throw new Error('Invalid padding');
        }

        return result;
    }

    function encode(hrp, data, spec) {
        const checksum = createChecksum(hrp, data, spec);
        const combined = data.concat(checksum);
        let result = hrp + '1';
        for (let d of combined) {
            result += CHARSET[d];
        }
        return result;
    }

    /**
     * Encode a witness program as a Bech32m address (for Taproot/SegWit v1+)
     * @param {string} hrp - Human readable part ('bc' for mainnet, 'tb' for testnet)
     * @param {number} witnessVersion - Witness version (1 for Taproot)
     * @param {Uint8Array|Array} witnessProgram - The witness program (32 bytes for Taproot)
     * @returns {string} Bech32m encoded address
     */
    function encodeSegwitAddress(hrp, witnessVersion, witnessProgram) {
        // Convert 8-bit witness program to 5-bit groups
        const programBytes = Array.from(witnessProgram);
        const data5bit = convertBits(programBytes, 8, 5, true);

        // Prepend witness version
        const fullData = [witnessVersion].concat(data5bit);

        // Use bech32m for witness version 1+ (Taproot)
        const spec = witnessVersion === 0 ? 'bech32' : 'bech32m';

        return encode(hrp, fullData, spec);
    }

    /**
     * Convert a hex string to Uint8Array
     */
    function hexToBytes(hex) {
        const bytes = new Uint8Array(hex.length / 2);
        for (let i = 0; i < hex.length; i += 2) {
            bytes[i / 2] = parseInt(hex.substr(i, 2), 16);
        }
        return bytes;
    }

    /**
     * Derive a Bitcoin Taproot address (bc1p...) from a Nostr pubkey
     * Nostr pubkeys are 32-byte x-only public keys, which is exactly what Taproot uses
     * @param {string} pubkeyHex - 32-byte (64 char) hex public key
     * @returns {string} Bitcoin Taproot address starting with bc1p
     */
    function nostrPubkeyToBitcoinAddress(pubkeyHex) {
        if (!pubkeyHex || pubkeyHex.length !== 64) {
            throw new Error('Invalid pubkey: must be 64 hex characters (32 bytes)');
        }

        const pubkeyBytes = hexToBytes(pubkeyHex);

        // Taproot uses witness version 1
        // The x-only pubkey (32 bytes) is used directly as the witness program
        return encodeSegwitAddress('bc', 1, pubkeyBytes);
    }

    // Export to global scope
    global.Bech32 = {
        encode: encode,
        encodeSegwitAddress: encodeSegwitAddress,
        nostrPubkeyToBitcoinAddress: nostrPubkeyToBitcoinAddress,
        hexToBytes: hexToBytes
    };

})(typeof window !== 'undefined' ? window : global);
