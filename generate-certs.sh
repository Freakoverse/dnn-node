#!/bin/bash
# DNN Certificate Generation Script
# Usage: ./generate-certs.sh <dnn_id>
# Example: ./generate-certs.sh nabceabsurd

set -e

# Check if DNN ID is provided
if [ -z "$1" ]; then
    echo "Usage: $0 <dnn_id>"
    echo "Example: $0 nabceabsurd"
    exit 1
fi

DNN_ID="$1"
CERT_DIR="${CERT_DIR:-/opt/certs}"
VALIDITY_DAYS="${VALIDITY_DAYS:-365000}"  # ~1000 years

echo "=== DNN Certificate Generation ==="
echo "DNN ID: $DNN_ID"
echo "Output directory: $CERT_DIR"
echo "Validity: $VALIDITY_DAYS days"
echo ""

# Create certificate directory
mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

# 1. Generate CA private key (if it doesn't exist)
if [ ! -f "dnn-ca.key" ]; then
    echo "[1/5] Generating CA private key..."
    openssl genrsa -out dnn-ca.key 4096
else
    echo "[1/5] Using existing CA private key..."
fi

# 2. Generate CA certificate (if it doesn't exist)
if [ ! -f "dnn-ca.crt" ]; then
    echo "[2/5] Generating CA certificate..."
    openssl req -x509 -new -nodes -key dnn-ca.key -sha256 -days 3650 \
      -out dnn-ca.crt -subj "/CN=DNN Certificate Authority"
else
    echo "[2/5] Using existing CA certificate..."
fi

# 3. Generate certificate signing request for the DNN ID
echo "[3/5] Generating certificate for $DNN_ID..."
openssl req -new -nodes -out "${DNN_ID}.csr" \
  -newkey rsa:2048 -keyout "${DNN_ID}.key" \
  -subj "/CN=${DNN_ID}"

# 4. Create SAN config with DNN ID
# This is crucial - the DNN ID must be in the SAN field
echo "[4/5] Creating SAN configuration..."
cat > "${DNN_ID}.ext" << EOF
subjectAltName = DNS:${DNN_ID},DNS:*.${DNN_ID}
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
EOF

# 5. Sign certificate with CA
echo "[5/5] Signing certificate..."
openssl x509 -req -in "${DNN_ID}.csr" -CA dnn-ca.crt -CAkey dnn-ca.key \
  -CAcreateserial -out "${DNN_ID}.crt" -days "$VALIDITY_DAYS" -sha256 \
  -extfile "${DNN_ID}.ext"

# Create combined PEM file for easy use
cat "${DNN_ID}.crt" "${DNN_ID}.key" > "${DNN_ID}.pem"
chmod 600 "${DNN_ID}.key" "${DNN_ID}.pem"

echo ""
echo "=== Certificate Generation Complete ==="
echo ""
echo "Files generated:"
echo "  - ${DNN_ID}.key     (Private key - keep secure!)"
echo "  - ${DNN_ID}.crt     (Certificate - use in connection event)"
echo "  - ${DNN_ID}.pem     (Combined key+cert for server use)"
echo "  - dnn-ca.crt        (CA certificate)"
echo ""
echo "The certificate contains your DNN ID '${DNN_ID}' in the Subject Alternative Name (SAN) field."
echo "DNN-aware browsers will verify that this certificate is associated with your DNN ID."
echo ""
echo "To view certificate details:"
echo "  openssl x509 -in ${DNN_ID}.crt -text -noout"
echo ""
echo "To copy the certificate for pasting into the dashboard:"
echo "  cat ${DNN_ID}.crt"
echo ""
ls -la "${CERT_DIR}/"
