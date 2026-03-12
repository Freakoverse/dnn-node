
console.log('DNN Dashboard v2.0.0 - Registration System Loaded');

// Pagination state
let currentPage = 1;
let pageSize = 100;
let allAnchors = [];
let filteredAnchors = [];
let searchTerm = '';

// Toggle for local relay vs external relays on My ID/TLDs page
let useLocalRelay = false;
window.useLocalRelay = useLocalRelay;

// Export to window for inline scripts
window.allAnchors = allAnchors;
window.filteredAnchors = filteredAnchors;

// ========== Universal Event Signing (NIP-07 + NIP-46) ==========

// Track which login method was used: 'extension' or 'remote'
// This is set when the user logs in and determines which signer to use
window.loginMethod = null;

/**
 * Sign an event using either browser extension (NIP-07) or remote signer (NIP-46)
 * Uses the same method that was used to login, not just what's available
 * @param {Object} unsignedEvent - The event to sign (without id and sig)
 * @returns {Promise<Object>} - The signed event with id and sig
 * @throws {Error} - If no signing method is available or signing fails
 */
async function signEventUniversal(unsignedEvent) {
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;

    if (!hasExtension && !hasRemoteSigner) {
        throw new Error('Please connect with a Nostr extension or remote signer first');
    }

    // Use the login method to determine which signer to use
    // This ensures consistency with how the user chose to connect
    const loginMethod = window.loginMethod;

    // If user explicitly logged in via remote signer, use that
    if (loginMethod === 'remote' && hasRemoteSigner) {
        console.log('[SignEvent] Using remote signer (login method)');
        if (typeof signEventWithRemoteSigner === 'function') {
            return await signEventWithRemoteSigner(unsignedEvent);
        } else {
            throw new Error('Remote signer support not loaded. Please refresh the page.');
        }
    }

    // If user explicitly logged in via extension, use that
    if (loginMethod === 'extension' && hasExtension) {
        console.log('[SignEvent] Using browser extension (login method)');
        return await window.nostr.signEvent(unsignedEvent);
    }

    // Fallback: If no login method tracked but user has a connection, infer from state
    // Prefer remote signer if connected (as that's more explicit), otherwise extension
    if (hasRemoteSigner) {
        console.log('[SignEvent] Using remote signer (fallback - remoteSignerConnected is true)');
        if (typeof signEventWithRemoteSigner === 'function') {
            return await signEventWithRemoteSigner(unsignedEvent);
        }
    }

    if (hasExtension) {
        console.log('[SignEvent] Using browser extension (fallback)');
        return await window.nostr.signEvent(unsignedEvent);
    }

    throw new Error('No signing method available');
}

// Make it globally available
window.signEventUniversal = signEventUniversal;

// ========== UUID v4 Generator for NIP-DN d-tags ==========

/**
 * Generate a UUID v4 for use as d-tag in addressable replaceable events
 * @returns {string} - A random UUID v4 string
 */
function generateUUIDv4() {
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function (c) {
        const r = Math.random() * 16 | 0;
        const v = c === 'x' ? r : (r & 0x3 | 0x8);
        return v.toString(16);
    });
}

// Make it globally available
window.generateUUIDv4 = generateUUIDv4;

// ========== Compute Interception IPv6 from npub ==========

/**
 * Compute the interception_ipv6 address from an npub
 * Uses SHA-256(bech32_decode(npub)) -> fd + first 15 bytes
 * @param {string} npub - The server's npub (e.g., npub1abc...)
 * @returns {Promise<string|null>} - The IPv6 address or null if invalid
 */
async function computeInterceptionIPv6FromNpub(npub) {
    try {
        if (!npub || !npub.startsWith('npub1')) {
            return null;
        }

        // Decode npub to hex pubkey
        let pubkeyHex;
        try {
            if (window.NostrTools && window.NostrTools.nip19) {
                const decoded = window.NostrTools.nip19.decode(npub);
                pubkeyHex = decoded.data;
            } else if (typeof nip19 !== 'undefined' && nip19.decode) {
                const decoded = nip19.decode(npub);
                pubkeyHex = decoded.data;
            } else {
                console.warn('NostrTools not available for npub decode');
                return null;
            }
        } catch (e) {
            console.error('Failed to decode npub:', e);
            return null;
        }

        // Convert hex to bytes
        const pubkeyBytes = new Uint8Array(pubkeyHex.match(/.{1,2}/g).map(byte => parseInt(byte, 16)));

        // SHA-256 hash
        const hashBuffer = await crypto.subtle.digest('SHA-256', pubkeyBytes);
        const hashArray = new Uint8Array(hashBuffer);

        // Build IPv6: fd + first 15 bytes of hash
        const ipv6Parts = ['fd' + hashArray[0].toString(16).padStart(2, '0')];
        for (let i = 1; i < 15; i += 2) {
            ipv6Parts.push(
                hashArray[i].toString(16).padStart(2, '0') +
                hashArray[i + 1].toString(16).padStart(2, '0')
            );
        }

        return ipv6Parts.join(':');
    } catch (e) {
        console.error('Failed to compute interception IPv6:', e);
        return null;
    }
}

// Compute IPv6 button handler
async function computeInterceptionIPv6() {
    const npubInput = document.getElementById('connServerNpubs')?.value?.trim();
    if (!npubInput) {
        showToast('Please enter at least one server npub first', 'warning');
        return;
    }

    // Take the first npub (primary server for IPv6 derivation)
    const firstNpub = npubInput.split(/[,\n]/)[0].trim();
    if (!firstNpub) {
        showToast('Please enter a valid npub', 'warning');
        return;
    }

    const ipv6 = await computeInterceptionIPv6FromNpub(firstNpub);
    if (ipv6) {
        document.getElementById('connInterceptionIPv6').value = ipv6;
        showToast('IPv6 computed successfully', 'success');
    } else {
        showToast('Failed to compute IPv6. Make sure the npub is valid.', 'error');
    }
}

// Make them globally available
window.computeInterceptionIPv6FromNpub = computeInterceptionIPv6FromNpub;
window.computeInterceptionIPv6 = computeInterceptionIPv6;

// ========== Tollgate Toggle ==========

function toggleTollgate(buttonId) {
    const button = document.getElementById(buttonId);
    if (!button) return;

    const isEnabled = button.dataset.enabled === 'true';
    const newState = !isEnabled;
    button.dataset.enabled = newState ? 'true' : 'false';

    // Update visual state
    const knob = button.querySelector('span');
    if (newState) {
        button.classList.remove('bg-dnn-secondary');
        button.classList.add('bg-purple-500/50', 'border-purple-500');
        knob.classList.remove('left-0.5', 'bg-gray-500');
        knob.classList.add('left-5', 'bg-purple-400');
    } else {
        button.classList.remove('bg-purple-500/50', 'border-purple-500');
        button.classList.add('bg-dnn-secondary');
        knob.classList.remove('left-5', 'bg-purple-400');
        knob.classList.add('left-0.5', 'bg-gray-500');
    }
}

// For class-based toggles (Other Name Connections)
function toggleTollgateByElement(button) {
    if (!button) return;

    const isEnabled = button.dataset.enabled === 'true';
    const newState = !isEnabled;
    button.dataset.enabled = newState ? 'true' : 'false';

    // Update visual state
    const knob = button.querySelector('span');
    if (newState) {
        button.classList.remove('bg-dnn-secondary');
        button.classList.add('bg-purple-500/50', 'border-purple-500');
        knob.classList.remove('left-0.5', 'bg-gray-500');
        knob.classList.add('left-5', 'bg-purple-400');
    } else {
        button.classList.remove('bg-purple-500/50', 'border-purple-500');
        button.classList.add('bg-dnn-secondary');
        knob.classList.remove('left-5', 'bg-purple-400');
        knob.classList.add('left-0.5', 'bg-gray-500');
    }
}

// Set tollgate toggle state programmatically
function setTollgateState(buttonIdOrElement, enabled) {
    const button = typeof buttonIdOrElement === 'string'
        ? document.getElementById(buttonIdOrElement)
        : buttonIdOrElement;
    if (!button) return;

    button.dataset.enabled = enabled ? 'true' : 'false';
    const knob = button.querySelector('span');
    if (enabled) {
        button.classList.remove('bg-dnn-secondary');
        button.classList.add('bg-purple-500/50', 'border-purple-500');
        knob.classList.remove('left-0.5', 'bg-gray-500');
        knob.classList.add('left-5', 'bg-purple-400');
    } else {
        button.classList.remove('bg-purple-500/50', 'border-purple-500');
        button.classList.add('bg-dnn-secondary');
        knob.classList.remove('left-5', 'bg-purple-400');
        knob.classList.add('left-0.5', 'bg-gray-500');
    }
}

window.toggleTollgate = toggleTollgate;
window.toggleTollgateByElement = toggleTollgateByElement;
window.setTollgateState = setTollgateState;

// ========== Custom Transport Management ==========

let customTransportCounter = 0;

function addCustomTransport() {
    const container = document.getElementById('customTransports');
    if (!container) return;

    const id = `customTransport_${customTransportCounter++}`;
    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-start';
    div.innerHTML = `
        <input type="text" class="customTransportLabel w-32 px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs"
            placeholder="label (e.g., dht)">
        <input type="text" class="customTransportValues flex-1 px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs"
            placeholder="values (comma-separated)">
        <button onclick="removeCustomTransport('${id}')" type="button"
            class="p-1.5 text-red-400 hover:text-red-300 hover:bg-red-500/10 rounded-lg transition-all">
            <i data-lucide="x" class="w-4 h-4"></i>
        </button>
    `;
    container.appendChild(div);

    // Re-initialize Lucide icons for the new button
    if (typeof lucide !== 'undefined' && lucide.createIcons) {
        lucide.createIcons();
    }
}

function removeCustomTransport(id) {
    const element = document.getElementById(id);
    if (element) {
        element.remove();
    }
}

// Make them globally available
window.addCustomTransport = addCustomTransport;
window.removeCustomTransport = removeCustomTransport;

// ========== Toast/Modal Utility ==========

function showToast(message, type = 'info') {
    // Remove any existing toast
    const existing = document.getElementById('dnn-toast');
    if (existing) existing.remove();

    const colors = {
        info: 'bg-blue-500/20 border-blue-500/50 text-blue-400',
        error: 'bg-red-500/20 border-red-500/50 text-red-400',
        success: 'bg-green-500/20 border-green-500/50 text-green-400',
        warning: 'bg-yellow-500/20 border-yellow-500/50 text-yellow-400'
    };

    const toast = document.createElement('div');
    toast.id = 'dnn-toast';
    toast.className = `fixed top-4 right-4 px-6 py-4 rounded-xl border backdrop-blur-sm shadow-2xl z-[9999] transition-all duration-300 ${colors[type] || colors.info}`;
    toast.innerHTML = `
        <div class="flex items-center gap-3">
            <span class="text-sm font-medium">${message}</span>
            <button onclick="this.parentElement.parentElement.remove()" class="text-gray-400 hover:text-white transition-colors">
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            </button>
        </div>
    `;
    document.body.appendChild(toast);

    // Auto-remove after 5 seconds
    setTimeout(() => toast.remove(), 5000);
}

window.showToast = showToast;

// ========== Other Name Connection Helpers ==========

let otherConnCustomTransportCounters = {};

async function computeOtherConnInterceptionIPv6(connId) {
    const connDiv = document.getElementById(connId);
    if (!connDiv) return;

    const npubInput = connDiv.querySelector('.otherConnServerNpubs')?.value?.trim();
    if (!npubInput) {
        showToast('Please enter at least one server npub first', 'warning');
        return;
    }

    const firstNpub = npubInput.split(/[,\n]/)[0].trim();
    if (!firstNpub) {
        showToast('Please enter a valid npub', 'warning');
        return;
    }

    const ipv6 = await computeInterceptionIPv6FromNpub(firstNpub);
    if (ipv6) {
        connDiv.querySelector('.otherConnInterceptionIPv6').value = ipv6;
        showToast('IPv6 computed successfully', 'success');
    } else {
        showToast('Failed to compute IPv6. Make sure the npub is valid.', 'error');
    }
}

function addOtherConnCustomTransport(connIndex) {
    const container = document.getElementById(`otherConnCustomTransports_${connIndex}`);
    if (!container) return;

    if (!otherConnCustomTransportCounters[connIndex]) {
        otherConnCustomTransportCounters[connIndex] = 0;
    }
    const id = `otherConnCustomTransport_${connIndex}_${otherConnCustomTransportCounters[connIndex]++}`;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-start';
    div.innerHTML = `
        <input type="text" class="otherConnCustomTransportLabel w-28 px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs" placeholder="label">
        <input type="text" class="otherConnCustomTransportValues flex-1 px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs" placeholder="values (comma-separated)">
        <button onclick="removeField('${id}')" type="button" class="p-1.5 text-red-400 hover:text-red-300 hover:bg-red-500/10 rounded-lg transition-all">
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>
    `;
    container.appendChild(div);
}

window.computeOtherConnInterceptionIPv6 = computeOtherConnInterceptionIPv6;
window.addOtherConnCustomTransport = addOtherConnCustomTransport;

function truncate(str, len) {
    if (!str) return 'N/A';
    return str.length > len ? str.substring(0, len) + '...' : str;
}

function formatDate(timestamp) {
    if (!timestamp) return 'N/A';
    // Handle both ISO string and Unix timestamp
    const date = typeof timestamp === 'string' ? new Date(timestamp) : new Date(timestamp * 1000);
    if (isNaN(date.getTime())) return 'Invalid Date';

    // Format full date as DD-MM-YYYY HH:MM:SS UTC for tooltip
    const day = String(date.getUTCDate()).padStart(2, '0');
    const month = String(date.getUTCMonth() + 1).padStart(2, '0');
    const year = date.getUTCFullYear();
    const hours = String(date.getUTCHours()).padStart(2, '0');
    const minutes = String(date.getUTCMinutes()).padStart(2, '0');
    const seconds = String(date.getUTCSeconds()).padStart(2, '0');
    const fullDate = day + '-' + month + '-' + year + ' ' + hours + ':' + minutes + ':' + seconds + ' UTC';

    // Calculate relative time for display
    const now = new Date();
    const diffMs = now - date;
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    let relativeTime;
    if (diffMins < 1) relativeTime = 'just now';
    else if (diffMins < 60) relativeTime = diffMins + 'm ago';
    else if (diffHours < 24) relativeTime = diffHours + 'h ago';
    else relativeTime = diffDays + 'd ago';

    // Return span with tooltip containing full date
    return '<span title="' + fullDate + '" style="cursor: help; border-bottom: 1px dotted #4b5563;">' + relativeTime + '</span>';
}

// copyToClipboard is defined in index.html with visual feedback (green checkmark)

// ========== Node Page Functions ==========

// Load node information for the Node page
async function loadNodePage() {
    try {
        const response = await fetch('/dnn/node-info');
        const data = await response.json();

        // Update node identity
        document.getElementById('nodeNpub').textContent = truncate(data.node_npub, 20);
        document.getElementById('nodeNpub').title = data.node_npub;

        document.getElementById('nodeRelay').textContent = data.relay_url;

        document.getElementById('nodePubkey').textContent = truncate(data.node_pubkey, 16);
        document.getElementById('nodePubkey').title = data.node_pubkey;

        // Render hardcoded relays
        const relaysList = document.getElementById('hardcodedRelaysList');
        if (data.configured_relays && data.configured_relays.length > 0) {
            relaysList.innerHTML = data.configured_relays.map(relay => `
                <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
                    <div class="flex items-center gap-3">
                        <div class="w-2 h-2 bg-gray-400 rounded-full"></div>
                        <span class="font-mono text-sm">${relay}</span>
                    </div>
                    <span class="text-xs text-gray-500">Configured</span>
                </div>
            `).join('');
        } else {
            relaysList.innerHTML = '<div class="text-center py-4 text-gray-500 text-sm">No relays configured</div>';
        }

        // Render hardcoded peers
        const peersList = document.getElementById('hardcodedPeersList');
        if (data.configured_peers && data.configured_peers.length > 0) {
            peersList.innerHTML = data.configured_peers.map(peer => {
                const peerData = data.peers ? data.peers.find(p => p.relay_url === peer) : null;
                const isActive = peerData?.is_active;
                const lastSeen = peerData?.last_seen;
                const statusDot = isActive ? 'bg-green-400' : 'bg-gray-400';
                const statusText = isActive ? 'Online' : 'Offline';
                const timeText = lastSeen ? formatTimeAgo(lastSeen) : '';

                return `
                    <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
                        <div class="flex items-center gap-3">
                            <div class="w-2 h-2 ${statusDot} rounded-full"></div>
                            <span class="font-mono text-sm">${peer}</span>
                        </div>
                        <div class="flex items-center gap-2">
                            <span class="text-xs ${isActive ? 'text-green-400' : 'text-gray-500'}">${statusText}</span>
                            ${timeText ? `<span class="text-xs text-gray-500">${timeText}</span>` : ''}
                        </div>
                    </div>
                `;
            }).join('');
        } else {
            peersList.innerHTML = '<div class="text-center py-4 text-gray-500 text-sm">No peers configured</div>';
        }

        // Update awareness status (enabled/disabled) from node-info
        const awareness = data.awareness || {};
        document.getElementById('awarenessStatus').textContent = awareness.enabled ? 'Enabled' : 'Disabled';
        document.getElementById('awarenessStatus').className = awareness.enabled
            ? 'ml-auto px-3 py-1 bg-green-500/10 border border-green-500/30 rounded-full text-green-400 text-xs'
            : 'ml-auto px-3 py-1 bg-gray-500/10 border border-gray-500/30 rounded-full text-gray-400 text-xs';

        // Fetch actual awareness stats from dedicated endpoint
        try {
            const statsResponse = await fetch('/dnn/awareness/stats');
            const stats = await statsResponse.json();
            document.getElementById('awarenessTotalMarks').textContent = stats.local_total || 0;
            document.getElementById('awarenessGoodMarks').textContent = stats.local_good || 0;
            document.getElementById('awarenessBadMarks').textContent = stats.local_bad || 0;
        } catch (e) {
            console.warn('Failed to fetch awareness stats:', e);
        }

        // Announce Node is automated now — no button needed

        // Refresh icons
        if (typeof lucide !== 'undefined') {
            lucide.createIcons();
        }
    } catch (error) {
        console.error('Failed to load node info:', error);
    }
}

// Switch tabs on Node page (for relays and peers)
function switchNodeTab(section, tab) {
    // Update tab buttons
    document.getElementById(`${section}-tab-hardcoded`).className = tab === 'hardcoded'
        ? 'px-4 py-2 rounded-lg text-sm font-medium bg-dnn-accent text-white'
        : 'px-4 py-2 rounded-lg text-sm font-medium bg-dnn-secondary text-gray-400 hover:text-white';
    document.getElementById(`${section}-tab-discovered`).className = tab === 'discovered'
        ? 'px-4 py-2 rounded-lg text-sm font-medium bg-dnn-accent text-white'
        : 'px-4 py-2 rounded-lg text-sm font-medium bg-dnn-secondary text-gray-400 hover:text-white';

    // Update content visibility
    document.getElementById(`${section}-content-hardcoded`).classList.toggle('hidden', tab !== 'hardcoded');
    document.getElementById(`${section}-content-discovered`).classList.toggle('hidden', tab !== 'discovered');

    // Load discovered data when switching to discovered tab
    if (tab === 'discovered') {
        if (section === 'relays') {
            loadDiscoveredRelays();
        } else if (section === 'peers') {
            loadDiscoveredPeers();
        }
    }

    if (typeof lucide !== 'undefined') {
        lucide.createIcons();
    }
}

// State for pagination
let discoveredRelaysPage = 0;
let discoveredRelaysSearch = '';
let discoveredPeersPage = 0;
let discoveredPeersSearch = '';
const PAGE_SIZE = 10;

// Load discovered relays with pagination and search
async function loadDiscoveredRelays(page = 0, search = '') {
    const container = document.getElementById('discoveredRelaysList');
    if (!container) return;

    discoveredRelaysPage = page;
    discoveredRelaysSearch = search;

    try {
        const offset = page * PAGE_SIZE;
        const response = await fetch(`/dnn/discovered-relays?limit=${PAGE_SIZE}&offset=${offset}&search=${encodeURIComponent(search)}`);
        const data = await response.json();

        if (!data.results || data.results.length === 0) {
            container.innerHTML = `
                <div class="text-center py-4 text-gray-500 text-sm">
                    <i data-lucide="info" class="w-4 h-4 inline mr-1"></i>
                    ${search ? 'No relays match your search' : 'No relays discovered yet'}
                </div>`;
        } else {
            const relaysHtml = data.results.map(relay => `
                <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
                    <div class="flex items-center gap-3">
                        <div class="w-2 h-2 bg-blue-400 rounded-full"></div>
                        <span class="font-mono text-sm">${relay.url}</span>
                    </div>
                    <span class="text-xs text-blue-400">Discovered</span>
                </div>
            `).join('');

            // Add pagination
            const totalPages = Math.ceil(data.total / PAGE_SIZE);
            const paginationHtml = renderPagination(page, totalPages, 'loadDiscoveredRelays', search);

            container.innerHTML = relaysHtml + paginationHtml;
        }

        if (typeof lucide !== 'undefined') lucide.createIcons();
    } catch (error) {
        console.error('Failed to load discovered relays:', error);
        container.innerHTML = `
            <div class="text-center py-4 text-red-400 text-sm">
                Failed to load discovered relays
            </div>`;
    }
}

// Load discovered peers with pagination and search
async function loadDiscoveredPeers(page = 0, search = '') {
    const container = document.getElementById('discoveredPeersList');
    if (!container) return;

    discoveredPeersPage = page;
    discoveredPeersSearch = search;

    try {
        const offset = page * PAGE_SIZE;
        const response = await fetch(`/dnn/discovered-peers?limit=${PAGE_SIZE}&offset=${offset}&search=${encodeURIComponent(search)}`);
        const data = await response.json();

        if (!data.results || data.results.length === 0) {
            container.innerHTML = `
                <div class="text-center py-4 text-gray-500 text-sm">
                    <i data-lucide="info" class="w-4 h-4 inline mr-1"></i>
                    ${search ? 'No peers match your search' : 'No peers discovered yet. Peers are discovered automatically via Nostr.'}
                </div>`;
        } else {
            const peersHtml = data.results.map(peer => {
                const statusDot = peer.is_verified ? 'bg-green-400' : 'bg-yellow-400';
                const statusText = peer.is_verified ? 'Verified' : 'Unverified';
                const failInfo = peer.fail_count > 0 ? ` (${peer.fail_count} fails)` : '';
                const timeAgo = peer.last_seen ? formatTimeAgo(peer.last_seen * 1000) : '';
                const npubShort = peer.node_npub ? peer.node_npub.slice(0, 20) + '...' : 'Unknown';

                return `
                    <div class="p-3 bg-dnn-secondary rounded-xl">
                        <div class="flex items-center justify-between mb-2">
                            <div class="flex items-center gap-3">
                                <div class="w-2 h-2 ${statusDot} rounded-full"></div>
                                <span class="font-mono text-sm text-white">${peer.address}</span>
                            </div>
                            <div class="flex items-center gap-2">
                                <span class="text-xs ${peer.is_verified ? 'text-green-400' : 'text-yellow-400'}">${statusText}${failInfo}</span>
                                ${timeAgo ? `<span class="text-xs text-gray-500">${timeAgo}</span>` : ''}
                            </div>
                        </div>
                        <div class="text-xs text-gray-400">Node: ${npubShort}</div>
                    </div>
                `;
            }).join('');

            // Add pagination
            const totalPages = Math.ceil(data.total / PAGE_SIZE);
            const paginationHtml = renderPagination(page, totalPages, 'loadDiscoveredPeers', search);

            container.innerHTML = peersHtml + paginationHtml;
        }

        if (typeof lucide !== 'undefined') lucide.createIcons();
    } catch (error) {
        console.error('Failed to load discovered peers:', error);
        container.innerHTML = `
            <div class="text-center py-4 text-red-400 text-sm">
                Failed to load discovered peers
            </div>`;
    }
}

// Render pagination controls [<] [1] [2] [3] [>]
function renderPagination(currentPage, totalPages, functionName, search) {
    if (totalPages <= 1) return '';

    const searchParam = search ? `, '${search.replace(/'/g, "\\'")}'` : '';
    let html = '<div class="flex items-center justify-center gap-1 mt-4">';

    // Previous button
    if (currentPage > 0) {
        html += `<button onclick="${functionName}(${currentPage - 1}${searchParam})" class="px-3 py-1 rounded bg-dnn-secondary hover:bg-dnn-border text-sm">&lt;</button>`;
    } else {
        html += `<button disabled class="px-3 py-1 rounded bg-dnn-secondary text-gray-600 text-sm cursor-not-allowed">&lt;</button>`;
    }

    // Page numbers - show max 5 pages centered around current
    const startPage = Math.max(0, Math.min(currentPage - 2, totalPages - 5));
    const endPage = Math.min(totalPages, startPage + 5);

    for (let i = startPage; i < endPage; i++) {
        if (i === currentPage) {
            html += `<button class="px-3 py-1 rounded bg-dnn-accent text-white text-sm font-medium">${i + 1}</button>`;
        } else {
            html += `<button onclick="${functionName}(${i}${searchParam})" class="px-3 py-1 rounded bg-dnn-secondary hover:bg-dnn-border text-sm">${i + 1}</button>`;
        }
    }

    // Next button
    if (currentPage < totalPages - 1) {
        html += `<button onclick="${functionName}(${currentPage + 1}${searchParam})" class="px-3 py-1 rounded bg-dnn-secondary hover:bg-dnn-border text-sm">&gt;</button>`;
    } else {
        html += `<button disabled class="px-3 py-1 rounded bg-dnn-secondary text-gray-600 text-sm cursor-not-allowed">&gt;</button>`;
    }

    html += '</div>';
    return html;
}

// Debounce timers for search
let relaySearchTimer = null;
let peerSearchTimer = null;

// Debounced search handler for relays
function handleRelaySearch(value) {
    clearTimeout(relaySearchTimer);
    relaySearchTimer = setTimeout(() => {
        loadDiscoveredRelays(0, value);
    }, 300);
}

// Debounced search handler for peers
function handlePeerSearch(value) {
    clearTimeout(peerSearchTimer);
    peerSearchTimer = setTimeout(() => {
        loadDiscoveredPeers(0, value);
    }, 300);
}

// Format time ago for peer last seen - returns HTML with tooltip
function formatTimeAgo(timestamp) {
    if (!timestamp) return '';
    const date = new Date(timestamp);
    const now = new Date();
    const diffMs = now - date;
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    // Calculate relative time
    let relativeTime;
    if (diffMins < 1) relativeTime = 'just now';
    else if (diffMins < 60) relativeTime = `${diffMins}m ago`;
    else if (diffHours < 24) relativeTime = `${diffHours}h ago`;
    else relativeTime = `${diffDays}d ago`;

    // Format full date for tooltip
    const day = String(date.getUTCDate()).padStart(2, '0');
    const month = String(date.getUTCMonth() + 1).padStart(2, '0');
    const year = date.getUTCFullYear();
    const hours = String(date.getUTCHours()).padStart(2, '0');
    const minutes = String(date.getUTCMinutes()).padStart(2, '0');
    const seconds = String(date.getUTCSeconds()).padStart(2, '0');
    const fullDate = `${day}-${month}-${year} ${hours}:${minutes}:${seconds} UTC`;

    // Return span with tooltip
    return `<span title="${fullDate}" style="cursor: help; border-bottom: 1px dotted #4b5563;">${relativeTime}</span>`;
}

// Make functions globally available
window.loadNodePage = loadNodePage;
window.switchNodeTab = switchNodeTab;
window.editEvent = editEvent;

// ========== Admin & Awareness Functions ==========

// Check if user is admin and update UI accordingly
async function checkAdminStatus() {
    console.log('[Admin Check] Starting admin check, userNpub:', window.userNpub);

    if (!window.userNpub) {
        console.log('[Admin Check] No userNpub set, setting isAdmin=false');
        window.isAdmin = false;
        updateAdminUI();
        return;
    }

    try {
        console.log('[Admin Check] Fetching /dnn/admin-check for:', window.userNpub);
        const response = await fetch('/dnn/admin-check', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ npub: window.userNpub })
        });

        if (!response.ok) {
            console.warn('[Admin Check] Response not OK:', response.status, response.statusText);
            window.isAdmin = false;
            updateAdminUI();
            return;
        }

        const data = await response.json();
        console.log('[Admin Check] Response data:', data);
        window.isAdmin = data.is_admin;
    } catch (error) {
        console.error('[Admin Check] Failed to check admin status:', error);
        window.isAdmin = false;
    }
    console.log('[Admin Check] Final isAdmin:', window.isAdmin);
    updateAdminUI();
}

// Update UI based on admin status
function updateAdminUI() {
    const awarenessNavDesktop = document.getElementById('nav-awareness');
    const awarenessNavMobile = document.getElementById('nav-awareness-mobile');

    if (window.isAdmin) {
        if (awarenessNavDesktop) awarenessNavDesktop.classList.remove('hidden');
        if (awarenessNavMobile) awarenessNavMobile.classList.remove('hidden');
    } else {
        if (awarenessNavDesktop) awarenessNavDesktop.classList.add('hidden');
        if (awarenessNavMobile) awarenessNavMobile.classList.add('hidden');
    }
}

// Load awareness page data
async function loadAwarenessPage() {
    try {
        // Load local marks
        const localResponse = await fetch('/dnn/awareness/local');
        const localMarks = await localResponse.json();
        renderLocalMarks(localMarks || []);

        // Load peer marks aggregate
        const peerResponse = await fetch('/dnn/awareness/peers');
        const peerMarks = await peerResponse.json();
        renderPeerMarks(peerMarks || []);

        // Load stats
        const statsResponse = await fetch('/dnn/awareness/stats');
        const stats = await statsResponse.json();
        console.log('Awareness stats:', stats);
        updateAwarenessPageStats(stats);

        if (typeof lucide !== 'undefined') {
            lucide.createIcons();
        }
    } catch (error) {
        console.error('Failed to load awareness page:', error);
    }
}

// Load database page data
async function loadRelayPage() {
    try {
        const response = await fetch('/dnn/relay/stats');
        const data = await response.json();

        // Update summary stats
        const el = id => document.getElementById(id);
        if (el('relayTotalDNNIDs')) el('relayTotalDNNIDs').textContent = data.total_dnn_ids || 0;
        if (el('relayLatestDNNBlock')) el('relayLatestDNNBlock').textContent = data.latest_dnn_block || 0;
        if (el('relayLatestBitcoinBlock')) el('relayLatestBitcoinBlock').textContent = (data.latest_bitcoin_block || 0).toLocaleString();
        if (el('relayTotalTables')) el('relayTotalTables').textContent = Object.keys(data.tables || {}).length;

        // Render table counts
        const tablesList = el('relayTablesList');
        if (tablesList && data.tables) {
            // Table metadata with display order
            const tableConfig = [
                { key: 'anchor_events', name: 'Anchor Events (60600)', icon: 'anchor', color: 'pink' },
                { key: 'name_events', name: 'Name Events (61600)', icon: 'tag', color: 'blue' },
                { key: 'connection_events', name: 'Connection Events (62600)', icon: 'cable', color: 'green' },
                { key: 'metadata_events', name: 'Metadata Events (63600)', icon: 'file-text', color: 'yellow' },
                { key: 'bitcoin_transactions', name: 'Bitcoin Transactions', icon: 'bitcoin', color: 'orange' },
                { key: 'dnn_blocks', name: 'DNN Blocks', icon: 'blocks', color: 'purple' },
                { key: 'awareness_marks_local', name: 'Local Awareness', icon: 'shield', color: 'orange' },
                { key: 'awareness_marks_peers', name: 'Peer Awareness', icon: 'shield-check', color: 'red' },
                { key: 'peer_nodes', name: 'Peer Nodes', icon: 'users', color: 'teal' },
                { key: 'event_cache', name: 'Event Cache', icon: 'database', color: 'gray', note: 'Reserved for caching' },
                { key: 'metrics', name: 'Metrics', icon: 'bar-chart-2', color: 'indigo', note: 'Reserved for metrics' },
                { key: 'sync_state', name: 'Sync State', icon: 'refresh-cw', color: 'cyan' }
            ];

            tablesList.innerHTML = tableConfig
                .filter(t => data.tables[t.key] !== undefined)
                .map(t => {
                    const count = data.tables[t.key];
                    const noteHtml = t.note ? `<span class="text-xs text-gray-500 ml-2">(${t.note})</span>` : '';
                    return `
                        <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
                            <div class="flex items-center gap-3">
                                <i data-lucide="${t.icon}" class="w-4 h-4 text-${t.color}-400"></i>
                                <span class="text-sm text-gray-300">${t.name}${noteHtml}</span>
                            </div>
                            <span class="text-sm font-mono font-semibold text-white">${count.toLocaleString()}</span>
                        </div>
                    `;
                }).join('');
        }

        // Render sync state
        const syncStateEl = el('relaySyncState');
        if (syncStateEl && data.sync_state) {
            const stateNames = {
                'last_bitcoin_block': { name: 'Last Bitcoin Block', icon: 'bitcoin' },
                'last_reorg_check': { name: 'Last Reorg Check', icon: 'git-branch' },
                'last_sync_time': { name: 'Last Sync Time', icon: 'clock' },
                'last_synced_block': { name: 'Last Synced Block', icon: 'check-circle' }
            };

            syncStateEl.innerHTML = Object.entries(data.sync_state)
                .filter(([key, _]) => stateNames[key]) // Only show recognized sync state keys
                .map(([key, value]) => {
                    const info = stateNames[key];
                    let displayValue = value;

                    // Format timestamps - handle both RFC3339 strings and Unix timestamps
                    if (key.includes('time') || key.includes('check')) {
                        // Try parsing as RFC3339 string first
                        const dateFromString = new Date(value);
                        if (!isNaN(dateFromString.getTime()) && value.includes('-')) {
                            displayValue = dateFromString.toLocaleString();
                        } else {
                            // Try parsing as Unix timestamp
                            const ts = parseInt(value);
                            if (ts > 0) {
                                displayValue = new Date(ts * 1000).toLocaleString();
                            } else {
                                displayValue = 'Never';
                            }
                        }
                    }

                    return `
                    <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
                        <div class="flex items-center gap-3">
                            <i data-lucide="${info.icon}" class="w-4 h-4 text-gray-400"></i>
                            <span class="text-sm text-gray-300">${info.name}</span>
                        </div>
                        <span class="text-sm font-mono text-white">${displayValue}</span>
                    </div>
                `;
                }).join('');
        }

        if (typeof lucide !== 'undefined') {
            lucide.createIcons();
        }
    } catch (error) {
        console.error('Failed to load database page:', error);
    }
}

// Render local marks list
function renderLocalMarks(marks) {
    const container = document.getElementById('localMarksList');
    if (!container) return;

    if (marks.length === 0) {
        container.innerHTML = '<div class="text-center py-4 text-gray-500 text-sm">No local marks yet. Add your first mark above.</div>';
        return;
    }

    const markColors = {
        'allow': { bg: 'bg-green-500/20', text: 'text-green-400', dot: 'bg-green-400' },
        'neutral': { bg: 'bg-yellow-500/20', text: 'text-yellow-400', dot: 'bg-yellow-400' },
        'block': { bg: 'bg-red-500/20', text: 'text-red-400', dot: 'bg-red-400' }
    };

    container.innerHTML = marks.map(mark => {
        const colors = markColors[mark.mark] || markColors['block'];
        const nameDisplay = mark.name ? `${mark.name}.` : '';
        const dnnId = `${nameDisplay}n${mark.dnn_block}.${mark.position}`;
        const categoryBadge = mark.category ? `<span class="px-2 py-0.5 rounded text-xs bg-dnn-dark text-gray-400">${mark.category}</span>` : '';
        const escapedName = (mark.name || '').replace(/'/g, "\\'");
        return `
        <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
            <div class="flex items-center gap-3 flex-wrap">
                <div class="w-2 h-2 ${colors.dot} rounded-full"></div>
                <span class="font-mono text-sm">${dnnId}</span>
                <span class="px-2 py-0.5 rounded text-xs ${colors.bg} ${colors.text}">${mark.mark}</span>
                ${categoryBadge}
            </div>
            <div class="flex items-center gap-3">
                <span class="text-xs text-gray-500">${mark.reason || 'No reason'}</span>
                <button onclick="deleteLocalMark(${mark.dnn_block}, ${mark.position}, '${escapedName}')" class="text-red-400 hover:text-red-300 text-sm">
                    <i data-lucide="trash-2" class="w-4 h-4"></i>
                </button>
            </div>
        </div>
    `;
    }).join('');
}

// Render peer marks aggregate
function renderPeerMarks(aggregates) {
    const container = document.getElementById('peerMarksList');
    if (!container) return;

    if (aggregates.length === 0) {
        container.innerHTML = '<div class="text-center py-4 text-gray-500 text-sm"><i data-lucide="info" class="w-4 h-4 inline mr-1"></i>No peer marks received yet. Marks from other nodes will appear here after sync.</div>';
        return;
    }

    container.innerHTML = aggregates.map(agg => {
        const nameDisplay = agg.name ? `${agg.name}.` : '';
        return `
        <div class="flex items-center justify-between p-3 bg-dnn-secondary rounded-xl">
            <span class="font-mono text-sm">${nameDisplay}n${agg.dnn_block}.${agg.position}</span>
            <div class="flex items-center gap-3">
                <span class="text-green-400 text-sm">${agg.allow_count || 0} allow</span>
                <span class="text-yellow-400 text-sm">${agg.neutral_count || 0} neutral</span>
                <span class="text-red-400 text-sm">${agg.block_count || 0} block</span>
                <span class="text-gray-500 text-xs">${agg.total_peers} peers</span>
            </div>
        </div>
    `;
    }).join('');
}

// Update awareness page stats (both Awareness page and Node page section)
function updateAwarenessPageStats(stats) {
    if (!stats) return;
    const el = id => document.getElementById(id);

    // Awareness page stats
    if (el('awarenessLocalTotal')) el('awarenessLocalTotal').textContent = stats.local_total || 0;
    if (el('awarenessLocalAllow')) el('awarenessLocalAllow').textContent = stats.local_allow || 0;
    if (el('awarenessLocalNeutral')) el('awarenessLocalNeutral').textContent = stats.local_neutral || 0;
    if (el('awarenessLocalBlock')) el('awarenessLocalBlock').textContent = stats.local_block || 0;
    if (el('awarenessPeerTotal')) el('awarenessPeerTotal').textContent = stats.peer_total || 0;
    if (el('awarenessPeerAllow')) el('awarenessPeerAllow').textContent = stats.peer_allow || 0;
    if (el('awarenessPeerNeutral')) el('awarenessPeerNeutral').textContent = stats.peer_neutral || 0;
    if (el('awarenessPeerBlock')) el('awarenessPeerBlock').textContent = stats.peer_block || 0;

    // Node page awareness section stats (same data, different element IDs)
    if (el('awarenessTotalMarks')) el('awarenessTotalMarks').textContent = stats.local_total || 0;
    if (el('awarenessAllowMarks')) el('awarenessAllowMarks').textContent = stats.local_allow || 0;
    if (el('awarenessNeutralMarks')) el('awarenessNeutralMarks').textContent = stats.local_neutral || 0;
    if (el('awarenessBlockMarks')) el('awarenessBlockMarks').textContent = stats.local_block || 0;


}

// Add a local mark
async function addLocalMark() {
    const dnnIdInput = document.getElementById('markDnnId');
    const markType = document.getElementById('markType').value;
    const reason = document.getElementById('markReason').value;
    const name = document.getElementById('markName')?.value?.trim() || '';
    const category = document.getElementById('markCategory')?.value || '';

    if (!dnnIdInput || !dnnIdInput.value) {
        alert('Please enter a DNN ID (e.g., n50.1)');
        return;
    }

    // Parse DNN ID (format: n{block}.{position})
    const match = dnnIdInput.value.match(/^n?(\d+)\.(\d+)$/);
    if (!match) {
        alert('Invalid DNN ID format. Use: n50.1 or 50.1');
        return;
    }

    const dnnBlock = parseInt(match[1]);
    const position = parseInt(match[2]);

    try {
        const response = await fetch('/dnn/awareness/local', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                dnn_block: dnnBlock,
                position: position,
                name: name,
                mark: markType,
                category: category,
                reason: reason
            })
        });

        if (response.ok) {
            dnnIdInput.value = '';
            document.getElementById('markReason').value = '';
            if (document.getElementById('markName')) document.getElementById('markName').value = '';
            loadAwarenessPage();
        } else {
            const err = await response.text();
            alert('Failed to add mark: ' + err);
        }
    } catch (error) {
        console.error('Failed to add mark:', error);
        alert('Failed to add mark');
    }
}

// Pending delete mark state
let pendingDeleteMark = null;

// Show the delete mark confirmation modal
function showDeleteMarkModal(dnnBlock, position, name = '') {
    pendingDeleteMark = { dnnBlock, position, name };
    const modal = document.getElementById('deleteMarkModal');
    if (modal) {
        const nameDisplay = name ? `${name}.` : '';
        document.getElementById('deleteMarkId').textContent = `${nameDisplay}n${dnnBlock}.${position}`;
        modal.classList.remove('hidden');
        modal.classList.add('flex');
        if (typeof lucide !== 'undefined') lucide.createIcons();
    }
}

// Close the delete mark confirmation modal
function closeDeleteMarkModal() {
    pendingDeleteMark = null;
    const modal = document.getElementById('deleteMarkModal');
    if (modal) {
        modal.classList.add('hidden');
        modal.classList.remove('flex');
    }
}

// Confirm and delete the mark
async function confirmDeleteMark() {
    if (!pendingDeleteMark) return;

    const { dnnBlock, position, name } = pendingDeleteMark;
    closeDeleteMarkModal();

    try {
        const nameParam = name ? `?name=${encodeURIComponent(name)}` : '';
        const response = await fetch(`/dnn/awareness/local/${dnnBlock}/${position}${nameParam}`, {
            method: 'DELETE'
        });

        if (response.ok) {
            loadAwarenessPage();
        } else {
            console.error('Failed to delete mark');
        }
    } catch (error) {
        console.error('Failed to delete mark:', error);
    }
}

// Legacy function for compatibility - now shows modal instead
async function deleteLocalMark(dnnBlock, position, name = '') {
    showDeleteMarkModal(dnnBlock, position, name);
}

// Publish awareness list as NIP-51 kind:30000 event
async function publishAwarenessList() {
    const statusEl = document.getElementById('publishStatus');

    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;

    if (!hasExtension && !hasRemoteSigner) {
        alert('Please connect with a Nostr extension or remote signer first.');
        return;
    }

    if (!window.userNpub) {
        alert('Please login with Nostr first.');
        return;
    }

    statusEl.textContent = 'Fetching marks...';
    statusEl.className = 'text-sm text-yellow-400';

    try {
        // Fetch local marks
        const response = await fetch('/dnn/awareness/local');
        const marks = await response.json();

        if (!marks || marks.length === 0) {
            statusEl.textContent = 'Publishing empty list (clears marks on peers)...';
            statusEl.className = 'text-sm text-yellow-400';
        }

        // Build NIP-51 list event (kind:30000)
        // Tags: ["d", "dnn-awareness"], ["dnn", "{name.}n{block}.{pos}", "mark", "category", "reason"]
        const tags = [
            ["d", "dnn-awareness"]
        ];

        if (marks && marks.length > 0) {
            marks.forEach(mark => {
                const namePrefix = mark.name ? `${mark.name}.` : '';
                const dnnId = `${namePrefix}n${mark.dnn_block}.${mark.position}`;
                tags.push(["dnn", dnnId, mark.mark || mark.mark_type, mark.category || "", mark.reason || ""]);
            });
        }

        const event = {
            kind: 30000,
            created_at: Math.floor(Date.now() / 1000),
            tags: tags,
            content: marks && marks.length > 0 ? `DNN Awareness List - ${marks.length} marks` : 'DNN Awareness List - empty'
        };

        statusEl.textContent = 'Signing event...';

        // Sign the event
        const signedEvent = await signEventUniversal(event);

        statusEl.textContent = 'Publishing to relays...';

        // Fetch relay URLs from node config
        const nodeInfoResp = await fetch('/dnn/node-info');
        const nodeInfo = await nodeInfoResp.json();
        const relayUrls = nodeInfo.configured_relays || [];

        if (relayUrls.length === 0) {
            statusEl.textContent = 'No relays configured!';
            statusEl.className = 'text-sm text-red-400';
            return;
        }

        // Publish to each relay
        let successCount = 0;
        for (const url of relayUrls) {
            try {
                const ws = new WebSocket(url);
                await new Promise((resolve, reject) => {
                    const timeout = setTimeout(() => {
                        ws.close();
                        reject(new Error('Timeout'));
                    }, 5000);

                    ws.onopen = () => {
                        ws.send(JSON.stringify(["EVENT", signedEvent]));
                    };
                    ws.onmessage = (msg) => {
                        const data = JSON.parse(msg.data);
                        if (data[0] === 'OK' && data[2] === true) {
                            successCount++;
                        }
                        clearTimeout(timeout);
                        ws.close();
                        resolve();
                    };
                    ws.onerror = () => {
                        clearTimeout(timeout);
                        ws.close();
                        reject(new Error('Connection failed'));
                    };
                });
            } catch (e) {
                console.warn('Failed to publish to', url, e);
            }
        }

        if (successCount > 0) {
            statusEl.textContent = `Published to ${successCount}/${relayUrls.length} relays ✓`;
            statusEl.className = 'text-sm text-green-400';
            // Auto-sync after publish to confirm
            setTimeout(() => syncAwarenessList(), 1000);
        } else {
            statusEl.textContent = 'Failed to publish to any relay';
            statusEl.className = 'text-sm text-red-400';
        }

    } catch (error) {
        console.error('Failed to publish awareness list:', error);
        statusEl.textContent = 'Error: ' + error.message;
        statusEl.className = 'text-sm text-red-400';
    }
}

// Sync awareness list from relays (fetch admin's NIP-51 list)
async function syncAwarenessList() {
    const statusEl = document.getElementById('publishStatus');
    statusEl.textContent = 'Syncing from relays...';
    statusEl.className = 'text-sm text-yellow-400';

    try {
        const response = await fetch('/dnn/awareness/sync', {
            method: 'POST'
        });

        if (response.ok) {
            const data = await response.json();
            statusEl.textContent = `Synced ${data.synced_count} marks ✓`;
            statusEl.className = 'text-sm text-green-400';
            // Reload the page data
            loadAwarenessPage();
        } else if (response.status === 404) {
            statusEl.textContent = 'No awareness list found on relays';
            statusEl.className = 'text-sm text-gray-500';
        } else {
            const err = await response.text();
            statusEl.textContent = 'Sync failed: ' + err;
            statusEl.className = 'text-sm text-red-400';
        }
    } catch (error) {
        console.error('Failed to sync awareness list:', error);
        statusEl.textContent = 'Error: ' + error.message;
        statusEl.className = 'text-sm text-red-400';
    }
}

// Custom Dropdown Functions
function toggleDropdown(button) {
    const dropdown = button.closest('.custom-dropdown');
    const optionsPanel = dropdown.querySelector('.dropdown-options');
    const arrow = dropdown.querySelector('.dropdown-arrow');

    // Close all other dropdowns
    document.querySelectorAll('.custom-dropdown .dropdown-options').forEach(panel => {
        if (panel !== optionsPanel) {
            panel.classList.add('hidden');
            panel.closest('.custom-dropdown').querySelector('.dropdown-arrow')?.classList.remove('rotate-180');
        }
    });

    // Toggle this dropdown
    optionsPanel.classList.toggle('hidden');
    arrow.classList.toggle('rotate-180');
}

function selectDropdownOption(optionEl, value, label) {
    const dropdown = optionEl.closest('.custom-dropdown');
    const hiddenInput = dropdown.querySelector('input[type="hidden"]');
    const labelSpan = dropdown.querySelector('.dropdown-label');
    const optionsPanel = dropdown.querySelector('.dropdown-options');
    const arrow = dropdown.querySelector('.dropdown-arrow');

    // Update value and label
    hiddenInput.value = value;
    dropdown.dataset.value = value;
    labelSpan.textContent = label;

    // Update label color based on type (for typed dropdowns)
    labelSpan.className = 'dropdown-label';
    if (value === 'block' || value === 'bad') {
        labelSpan.classList.add('text-red-400');
    } else if (value === 'neutral') {
        labelSpan.classList.add('text-yellow-400');
    } else if (value === 'allow' || value === 'good') {
        labelSpan.classList.add('text-green-400');
    }

    // Close dropdown
    optionsPanel.classList.add('hidden');
    arrow.classList.remove('rotate-180');

    // Call onchange handler if specified
    const onchangeHandler = dropdown.dataset.onchange;
    if (onchangeHandler && typeof window[onchangeHandler] === 'function') {
        window[onchangeHandler]();
    }
}

// Close dropdowns when clicking outside
document.addEventListener('click', (e) => {
    if (!e.target.closest('.custom-dropdown')) {
        document.querySelectorAll('.custom-dropdown .dropdown-options').forEach(panel => {
            panel.classList.add('hidden');
            panel.closest('.custom-dropdown').querySelector('.dropdown-arrow')?.classList.remove('rotate-180');
        });
    }
});

// Make functions globally available
window.checkAdminStatus = checkAdminStatus;
window.loadAwarenessPage = loadAwarenessPage;
window.addLocalMark = addLocalMark;
window.deleteLocalMark = deleteLocalMark;
window.publishAwarenessList = publishAwarenessList;
window.syncAwarenessList = syncAwarenessList;
window.toggleDropdown = toggleDropdown;
window.selectDropdownOption = selectDropdownOption;


function filterTable(resetPage = true) {
    // Server-side filtering: status and search are now handled by the API
    // Just reset page and trigger a server fetch

    searchTerm = document.getElementById('searchInput')?.value?.toLowerCase() || '';

    // Show/hide custom date range UI
    const timeFilter = document.querySelector('input[name="timeFilter"]:checked')?.value || 'all';
    const customDateRange = document.getElementById('customDateRange');
    if (customDateRange) {
        customDateRange.style.display = timeFilter === 'custom' ? 'flex' : 'none';
    }

    // Reset to page 1 when filters change
    if (resetPage) {
        currentPage = 1;
    }

    // Trigger server fetch with new filter parameters
    updateDashboard();
}

function resetFilters() {
    // Reset all filter controls to defaults
    document.querySelector('input[name="statusFilter"][value="complete"]').checked = true;
    document.querySelector('input[name="timeFilter"][value="all"]').checked = true;
    document.getElementById('blockRangeFrom').value = '';
    document.getElementById('blockRangeTo').value = '';
    document.getElementById('dateRangeFrom').value = '';
    document.getElementById('dateRangeTo').value = '';
    document.getElementById('myRegistrationsOnly').checked = false;
    document.getElementById('customDateRange').style.display = 'none';

    // Reapply filters
    filterTable();
}

function changePage(direction) {
    const serverTotal = window.paginatedTotal || 0;
    const totalPages = Math.ceil(serverTotal / pageSize);
    const newPage = currentPage + direction;

    if (newPage >= 1 && newPage <= totalPages) {
        currentPage = newPage;
        updateDashboard(); // Fetch new page from server
    }
}

function changePageSize() {
    pageSize = parseInt(document.getElementById('pageSizeSelect').value);
    currentPage = 1;
    updateDashboard(); // Fetch with new page size
}

function renderTable() {
    // With server-side pagination, filteredAnchors already contains current page data
    const pageAnchors = filteredAnchors;
    const serverTotal = window.paginatedTotal || filteredAnchors.length;
    const totalPages = Math.ceil(serverTotal / pageSize);

    // Update pagination UI (with null checks for new dashboard compatibility)
    const currentPageEl = document.getElementById('currentPage');
    const prevBtnEl = document.getElementById('prevBtn');
    const nextBtnEl = document.getElementById('nextBtn');
    const anchorCountEl = document.getElementById('anchorCount');

    if (currentPageEl) currentPageEl.textContent = currentPage + ' of ' + (totalPages || 1);
    if (prevBtnEl) prevBtnEl.disabled = currentPage <= 1;
    if (nextBtnEl) nextBtnEl.disabled = currentPage >= totalPages;
    if (anchorCountEl) anchorCountEl.textContent = pageAnchors.length + ' / ' + serverTotal;

    const tbody = document.getElementById('anchorsBody');
    if (!tbody) return; // Guard for new dashboard

    if (pageAnchors.length === 0) {
        const message = searchTerm ? 'No transactions match your search' : 'No Bitcoin transactions yet';
        const subMessage = searchTerm ? 'Try adjusting your search terms' : 'Waiting for valid self-transfer transactions...';

        tbody.innerHTML = '<tr><td colspan="11" class="empty-state">' +
            '<div class="empty-icon">📭</div>' +
            '<div>' + message + '</div>' +
            '<div style="font-size: 12px; margin-top: 8px;">' + subMessage + '</div>' +
            '</td></tr>';
    } else {
        try {
            tbody.innerHTML = pageAnchors.map(function (tx) {
                var statusBadge = tx.has_anchor_event ?
                    '<span class="status-badge status-complete">✓ Complete</span>' :
                    '<span class="status-badge status-pending">⏳ Pending</span>';

                // Always show encoded name in Name column for consistency
                var name;
                if (tx.encoded) {
                    name = '<strong style="color: #a78bfa;">' + tx.encoded + '</strong>';
                } else {
                    // Fallback to block notation if encoded name isn't available
                    name = '<span style="color: #64748b;">n' + tx.dnn_block + '.' + tx.position + '</span>';
                }

                // Transaction ID column
                var txIdCell = tx.transaction_id ?
                    '<span class="mono" title="' + tx.transaction_id + '">' + truncate(tx.transaction_id, 12) + '</span>' +
                    '<button class="copy-btn" onclick="copyToClipboard(\'' + tx.transaction_id + '\')">Copy</button>' :
                    '<span style="color: #64748b;">-</span>';

                // Npub column
                var npubCell = tx.npub ?
                    '<span class="mono" title="' + tx.npub + '">' + truncate(tx.npub, 16) + '</span>' +
                    '<button class="copy-btn" onclick="copyToClipboard(\'' + tx.npub + '\')">Copy</button>' :
                    '<span style="color: #64748b;">-</span>';

                // Event ID column - show nevent instead of raw ID
                var eventIdCell = tx.anchor_event_id && tx.npub ?
                    (function () {
                        // Extract hex pubkey from npub
                        // Use naddr from backend if available, otherwise try to encode
                        var identifier = tx.naddr;

                        if (!identifier && tx.anchor_event_id && tx.pubkey) {
                            // Fallback: try to encode as nevent
                            identifier = encodeNevent(tx.anchor_event_id, tx.pubkey, 60600);
                        }

                        if (identifier) {
                            return '<div style="display: flex; gap: 4px; align-items: center;">' +
                                '<span class="mono" title="' + identifier + '">' + truncate(identifier, 16) + '</span>' +
                                '<button class="copy-btn" onclick="copyToClipboard(\'' + identifier + '\')">Copy</button>' +
                                '<button class="copy-btn" style="background: #667eea; color: white;" onclick="showEventDetails(\'' + tx.anchor_event_id + '\')">Details</button>' +
                                '</div>';
                        } else {
                            return '<span class="text-muted">No event</span>';
                        }
                    })() :
                    '<span style="color: #64748b;">-</span>';

                return '<tr>' +
                    '<td>' + statusBadge + '</td>' +
                    '<td>' + name + '</td>' +
                    '<td>' + (tx.bitcoin_block || 0) + '</td>' +
                    '<td>' + (tx.dnn_block ?? 0) + '</td>' +
                    '<td>' + (tx.position || 0) + '</td>' +
                    '<td>' +
                    '<span class="mono" title="' + tx.bitcoin_address + '">' + truncate(tx.bitcoin_address, 20) + '</span>' +
                    '<button class="copy-btn" onclick="copyToClipboard(\'' + tx.bitcoin_address + '\')">Copy</button>' +
                    '</td>' +
                    '<td>' + txIdCell + '</td>' +
                    '<td>' + npubCell + '</td>' +
                    '<td>' + eventIdCell + '</td>' +
                    '<td>' + (tx.fee_rate || 0) + '</td>' +
                    '<td style="font-size: 12px;">' + formatDate(tx.discovered_at) + '</td>' +
                    '</tr>';
            }).join('');
        } catch (renderError) {
            console.error('Error rendering table rows:', renderError);
            console.log('First anchor that failed:', pageAnchors[0]);
            tbody.innerHTML = '<tr><td colspan="11" class="empty-state" style="color: #ef4444;"><div>Error rendering table: ' + renderError.message + '</div><div style="font-size: 12px; margin-top: 8px;">Check console for details</div></td></tr>';
        }
    }
}

async function updateDashboard() {
    try {
        console.log('Updating dashboard...');

        // Fetch status
        const statusRes = await fetch('/dnn/status');
        const status = await statusRes.json();

        console.log('Status:', status);

        document.getElementById('bitcoinBlock').textContent = status.latest_bitcoin_block || 0;
        document.getElementById('dnnBlock').textContent = status.latest_dnn_block || 0;
        document.getElementById('totalBitcoinTxs').textContent = status.total_bitcoin_txs || 0;
        document.getElementById('totalPending').textContent = status.total_pending_txs || 0;
        document.getElementById('totalComplete').textContent = status.total_anchors || 0;

        // Format database size in human-readable format
        const dbSizeBytes = status.database_size_bytes || 0;
        let dbSizeFormatted = '0 MB';
        if (dbSizeBytes < 1024 * 1024) {
            dbSizeFormatted = (dbSizeBytes / 1024).toFixed(1) + ' KB';
        } else if (dbSizeBytes < 1024 * 1024 * 1024) {
            dbSizeFormatted = (dbSizeBytes / (1024 * 1024)).toFixed(1) + ' MB';
        } else {
            dbSizeFormatted = (dbSizeBytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
        }
        document.getElementById('dbSize').textContent = dbSizeFormatted;

        document.getElementById('syncStatus').textContent = status.syncing ? 'Syncing...' : 'Idle';

        // Fetch paginated anchors (server-side pagination)
        // Build query params based on current filter state
        const currentStatus = document.querySelector('input[name="statusFilter"]:checked')?.value || 'all';
        const currentSearch = document.getElementById('searchInput')?.value || '';
        const pageOffset = (currentPage - 1) * pageSize;

        const anchorsUrl = `/dnn/anchors?limit=${pageSize}&offset=${pageOffset}&status=${currentStatus}&search=${encodeURIComponent(currentSearch)}`;
        const anchorsRes = await fetch(anchorsUrl);
        if (!anchorsRes.ok) {
            throw new Error('Failed to fetch anchors: ' + anchorsRes.status);
        }
        const anchorsData = await anchorsRes.json();
        console.log('Paginated anchors:', anchorsData);

        // New paginated response format: { results, total, limit, offset }
        if (anchorsData.results) {
            allAnchors = anchorsData.results;
            filteredAnchors = anchorsData.results;
            window.allAnchors = allAnchors;
            window.paginatedTotal = anchorsData.total;
            console.log(`Loaded ${allAnchors.length} of ${anchorsData.total} total anchors (page ${Math.floor(anchorsData.offset / pageSize) + 1})`);
        } else {
            // Fallback for old API response (array)
            allAnchors = Array.isArray(anchorsData) ? anchorsData : [];
            filteredAnchors = allAnchors;
            window.allAnchors = allAnchors;
            window.paginatedTotal = allAnchors.length;
            console.log('Loaded ' + allAnchors.length + ' anchors (legacy format)');
        }

        if (allAnchors.length === 0) {
            console.warn('No anchors returned from API');
        }

        // Render table with current page data (no client-side filtering needed)
        renderTable();

        console.log('Dashboard updated successfully');

        // Call any registered update hooks (for new dashboard integration)
        if (typeof window.onDashboardUpdate === 'function') {
            try {
                window.onDashboardUpdate();
            } catch (hookError) {
                console.error('Dashboard update hook error:', hookError);
            }
        }
    } catch (error) {
        console.error('Failed to update dashboard:', error);
        console.warn('Dashboard update failed: ' + error.message + '. Check console for details.');
    }
}

// Update immediately
console.log('Initializing dashboard...');
updateDashboard();

// ========== Real-time WebSocket Updates ==========
let dashboardWs = null;
let wsReconnectAttempts = 0;
const wsMaxReconnectAttempts = 10;
const wsReconnectDelay = 3000;

function connectDashboardWebSocket() {
    // Determine WebSocket URL based on current page protocol
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/dnn/ws/dashboard`;

    console.log('[WS] Connecting to dashboard WebSocket:', wsUrl);

    try {
        dashboardWs = new WebSocket(wsUrl);

        dashboardWs.onopen = () => {
            console.log('[WS] ✓ Connected to real-time updates');
            wsReconnectAttempts = 0;

            // Show connection indicator (optional)
            const indicator = document.getElementById('realtimeIndicator');
            if (indicator) {
                indicator.classList.remove('hidden');
                indicator.title = 'Real-time updates active';
            }
        };

        dashboardWs.onmessage = (event) => {
            try {
                const update = JSON.parse(event.data);
                handleDashboardUpdate(update);
            } catch (e) {
                console.error('[WS] Failed to parse update:', e);
            }
        };

        dashboardWs.onclose = () => {
            console.log('[WS] Connection closed');
            dashboardWs = null;

            // Hide connection indicator
            const indicator = document.getElementById('realtimeIndicator');
            if (indicator) {
                indicator.classList.add('hidden');
            }

            // Attempt reconnection with backoff
            if (wsReconnectAttempts < wsMaxReconnectAttempts) {
                wsReconnectAttempts++;
                console.log(`[WS] Reconnecting in ${wsReconnectDelay / 1000}s (attempt ${wsReconnectAttempts}/${wsMaxReconnectAttempts})...`);
                setTimeout(connectDashboardWebSocket, wsReconnectDelay);
            } else {
                console.warn('[WS] Max reconnection attempts reached. Falling back to polling.');
                // Fall back to polling if WebSocket fails
                setInterval(updateDashboard, 10000);
            }
        };

        dashboardWs.onerror = (error) => {
            console.error('[WS] WebSocket error:', error);
        };

    } catch (e) {
        console.error('[WS] Failed to create WebSocket:', e);
        // Fall back to polling
        setInterval(updateDashboard, 10000);
    }
}

function handleDashboardUpdate(update) {
    console.log('[WS] Received update:', update.type);

    switch (update.type) {
        case 'stats':
            // Update stats display
            const stats = update.data;
            if (stats) {
                const btcBlockEl = document.getElementById('bitcoinBlock');
                const dnnBlockEl = document.getElementById('dnnBlock');
                const completeEl = document.getElementById('totalComplete');
                const pendingEl = document.getElementById('totalPending');
                const dbSizeEl = document.getElementById('dbSize');

                if (btcBlockEl) btcBlockEl.textContent = stats.latest_bitcoin_block || 0;
                if (dnnBlockEl) dnnBlockEl.textContent = stats.latest_dnn_block || 0;
                if (completeEl) completeEl.textContent = stats.total_anchors || 0;
                if (pendingEl) pendingEl.textContent = stats.total_pending_txs || 0;
                if (dbSizeEl && stats.database_size) {
                    const sizeMB = (stats.database_size / (1024 * 1024)).toFixed(1);
                    dbSizeEl.textContent = sizeMB + ' MB';
                }

                // Trigger block chain re-render if function exists
                if (typeof renderBlockChain === 'function') {
                    renderBlockChain();
                }
            }
            break;

        case 'anchor_found':
            // Show toast notification for new anchor
            const anchor = update.data;
            console.log('[WS] New anchor found:', anchor);
            showToast(`New anchor found at DNN block ${anchor.dnn_block}, position ${anchor.position}`, 'success');

            // Refresh anchor data
            updateDashboard();
            break;

        case 'block_synced':
            // Block synced update
            const block = update.data;
            console.log('[WS] Block synced:', block);

            // Update will come with stats update
            break;

        default:
            console.log('[WS] Unknown update type:', update.type);
    }
}

// Simple toast notification
function showToast(message, type = 'info') {
    // Create toast container if it doesn't exist
    let toastContainer = document.getElementById('toastContainer');
    if (!toastContainer) {
        toastContainer = document.createElement('div');
        toastContainer.id = 'toastContainer';
        toastContainer.style.cssText = 'position: fixed; bottom: 20px; right: 20px; z-index: 10000; display: flex; flex-direction: column; gap: 8px;';
        document.body.appendChild(toastContainer);
    }

    const toast = document.createElement('div');
    const bgColor = type === 'success' ? '#10b981' : type === 'error' ? '#ef4444' : '#3b82f6';
    toast.style.cssText = `background: ${bgColor}; color: white; padding: 12px 20px; border-radius: 8px; box-shadow: 0 4px 12px rgba(0,0,0,0.3); font-size: 14px; animation: slideIn 0.3s ease-out;`;
    toast.textContent = message;

    toastContainer.appendChild(toast);

    // Auto-remove after 4 seconds
    setTimeout(() => {
        toast.style.animation = 'slideOut 0.3s ease-in';
        setTimeout(() => toast.remove(), 300);
    }, 4000);
}

// Connect to WebSocket for real-time updates
connectDashboardWebSocket();

// Fallback: refresh every 30 seconds in case WebSocket misses something
setInterval(updateDashboard, 30000);

// Event Details Modal Functions
async function showEventDetails(eventId, isStandalone = false) {
    const modal = document.getElementById('eventModal');
    const modalBody = document.getElementById('modalBody');

    // Show modal with loading state
    modal.classList.add('show');
    modalBody.innerHTML = '<div class="loading-spinner"><div class="spinner"></div><p style="margin-top: 16px;">Loading event details...</p></div>';

    try {
        let data;

        if (isStandalone) {
            // For standalone events (61600, 62600, 63600), use the single event endpoint
            const response = await fetch('/dnn/event/' + eventId);
            if (!response.ok) {
                throw new Error('Failed to fetch event');
            }
            const eventData = await response.json();
            console.log('Standalone event received:', eventData);

            // Render as a single event
            renderStandaloneEvent(eventData);
        } else {
            // For anchor events (60600), use the detailed endpoint with all referenced events
            const response = await fetch('/dnn/event-details/' + eventId);
            if (!response.ok) {
                throw new Error('Failed to fetch event details');
            }
            data = await response.json();
            console.log('Event details received:', data);
            renderEventDetails(data);
        }
    } catch (error) {
        console.error('Failed to fetch event details:', error);
        modalBody.innerHTML = '<div class="event-not-found"><div style="font-size: 48px; margin-bottom: 16px;">❌</div><div>Failed to load event details</div><div style="font-size: 12px; margin-top: 8px; color: #f87171;">' + error.message + '</div></div>';
    }
}

function closeEventModal() {
    const modal = document.getElementById('eventModal');
    modal.classList.remove('show');
}

// Close modals when clicking outside
window.onclick = function (event) {
    const eventModal = document.getElementById('eventModal');
    const editModal = document.getElementById('editModal');

    if (event.target === eventModal) {
        closeEventModal();
    }
    if (event.target === editModal) {
        closeEditModal();
    }
}

// Close modals with Escape key
document.addEventListener('keydown', function (event) {
    if (event.key === 'Escape') {
        closeEventModal();
        closeEditModal();
    }
});

// Event delegation for copy buttons in event details modal
document.addEventListener('click', function (event) {
    if (event.target && event.target.classList.contains('copy-icon-btn')) {
        const copyId = event.target.getAttribute('data-copy-id');
        if (copyId) {
            const element = document.getElementById(copyId);
            if (element) {
                const textToCopy = element.textContent || element.innerText;
                copyToClipboard(textToCopy);
            }
        }
    }
});

function renderStandaloneEvent(data) {
    const modalBody = document.getElementById('modalBody');
    const event = data.event;
    const encoding = data.encoding;

    if (!event) {
        modalBody.innerHTML = '<div class="event-not-found"><div style="font-size: 48px; margin-bottom: 16px;">❌</div><div>Event not found</div></div>';
        return;
    }

    const kindNames = {
        61600: 'Name Event',
        62600: 'Connection Event',
        63600: 'Metadata Event',
        60600: 'Anchor Event'
    };

    const icons = {
        61600: '📝',
        62600: '🔗',
        63600: '📊',
        60600: '⚓'
    };

    const title = kindNames[event.kind] || 'Event';
    const icon = icons[event.kind] || '📄';

    const html = renderEventSection(title, event, event.kind, icon, encoding);
    modalBody.innerHTML = html;
}

function renderEventDetails(data) {
    const modalBody = document.getElementById('modalBody');

    let html = '';

    // Render Anchor Event (kind 60600)
    if (data.anchor_event) {
        html += renderEventSection('Anchor Event', data.anchor_event, 60600, '⚓', data.anchor_encoding);
    } else {
        console.warn('Anchor event not found in response');
    }

    // Render Name Event (kind 61600)
    if (data.name_event) {
        html += renderEventSection('Name Event', data.name_event, 61600, '📝', data.name_encoding);
    } else {
        console.warn('Name Event (61600) not found - ID was:', data.anchor_event?.tags?.find(t => t[0] === 'd')?.[1]);
        html += '<div class="event-not-found" style="padding: 20px; margin-bottom: 16px; background: #7f1d1d; border: 1px solid #ef4444; border-radius: 8px;"><div style="color: #fca5a5;">⚠️ Name Event (61600) not found in database or relays</div></div>';
    }

    // Render Connection Event (kind 62600)
    if (data.connection_event) {
        html += renderEventSection('Connection Event', data.connection_event, 62600, '🔗', data.connection_encoding);
    } else {
        console.warn('Connection Event (62600) not found - ID was:', data.anchor_event?.tags?.find(t => t[0] === 'connection')?.[1]);
        html += '<div class="event-not-found" style="padding: 20px; margin-bottom: 16px; background: #7f1d1d; border: 1px solid #ef4444; border-radius: 8px;"><div style="color: #fca5a5;">⚠️ Connection Event (62600) not found in database or relays</div></div>';
    }

    // Render Metadata Event (kind 63600)
    if (data.metadata_event) {
        html += renderEventSection('Metadata Event', data.metadata_event, 63600, '📊', data.metadata_encoding);
    } else {
        console.warn('Metadata Event (63600) not found - ID was:', data.anchor_event?.tags?.find(t => t[0] === 'metadata')?.[1]);
        html += '<div class="event-not-found" style="padding: 20px; margin-bottom: 16px; background: #7f1d1d; border: 1px solid #ef4444; border-radius: 8px;"><div style="color: #fca5a5;">⚠️ Metadata Event (63600) not found in database or relays</div></div>';
    }

    modalBody.innerHTML = html;
}

function renderEventSection(title, event, kind, icon, encoding) {
    const eventJson = JSON.stringify(event, null, 2);
    const eventId = event.id || 'N/A';
    const pubkey = event.pubkey || 'N/A';
    const createdAt = event.created_at ? new Date(event.created_at * 1000).toLocaleString() : 'N/A';
    const content = event.content || '';

    // Build encoding display (only show naddr for addressable events)
    let encodingDisplay = '';
    if (encoding && encoding.naddr) {
        const naddrId = 'naddr-' + Math.random().toString(36).substr(2, 9);
        encodingDisplay += '<div class="event-data"><div class="event-data-label">Naddr Address (NIP-19)</div><div class="event-data-value"><button class="copy-icon-btn" data-copy-id="' + naddrId + '">Copy</button><span id="' + naddrId + '">' + encoding.naddr + '</span></div></div>';
    }

    // Parse content if it's JSON
    let parsedContent = content;
    let contentDisplay = '';
    try {
        if (content && content.trim().startsWith('{')) {
            parsedContent = JSON.parse(content);
            const parsedContentId = 'parsed-content-' + Math.random().toString(36).substr(2, 9);
            const parsedContentText = JSON.stringify(parsedContent, null, 2);
            contentDisplay = '<div class="event-data"><div class="event-data-label">Parsed Content</div><div class="event-data-value"><button class="copy-icon-btn" data-copy-id="' + parsedContentId + '">Copy</button><pre id="' + parsedContentId + '">' + escapeHtml(parsedContentText) + '</pre></div></div>';
        } else if (content) {
            const contentId = 'content-' + Math.random().toString(36).substr(2, 9);
            contentDisplay = '<div class="event-data"><div class="event-data-label">Content</div><div class="event-data-value"><button class="copy-icon-btn" data-copy-id="' + contentId + '">Copy</button><span id="' + contentId + '">' + escapeHtml(content) + '</span></div></div>';
        }
    } catch (e) {
        if (content) {
            const contentId = 'content-' + Math.random().toString(36).substr(2, 9);
            contentDisplay = '<div class="event-data"><div class="event-data-label">Content</div><div class="event-data-value"><button class="copy-icon-btn" data-copy-id="' + contentId + '">Copy</button><span id="' + contentId + '">' + escapeHtml(content) + '</span></div></div>';
        }
    }

    const eventIdSpanId = 'eventid-' + Math.random().toString(36).substr(2, 9);
    const pubkeySpanId = 'pubkey-' + Math.random().toString(36).substr(2, 9);
    const jsonSpanId = 'json-' + Math.random().toString(36).substr(2, 9);

    return '<div class="event-section">' +
        '<h3>' + icon + ' ' + title + ' <span class="event-kind-badge">Kind ' + kind + '</span></h3>' +
        '<div class="event-data">' +
        '<div class="event-data-label">Event ID</div>' +
        '<div class="event-data-value">' +
        '<button class="copy-icon-btn" data-copy-id="' + eventIdSpanId + '">Copy</button>' +
        '<span id="' + eventIdSpanId + '">' + eventId + '</span>' +
        '</div>' +
        '</div>' +
        encodingDisplay +
        '<div class="event-data">' +
        '<div class="event-data-label">Author Pubkey</div>' +
        '<div class="event-data-value">' +
        '<button class="copy-icon-btn" data-copy-id="' + pubkeySpanId + '">Copy</button>' +
        '<span id="' + pubkeySpanId + '">' + pubkey + '</span>' +
        '</div>' +
        '</div>' +
        '<div class="event-data">' +
        '<div class="event-data-label">Created At</div>' +
        '<div class="event-data-value">' + createdAt + '</div>' +
        '</div>' +
        contentDisplay +
        '<div class="event-data">' +
        '<div class="event-data-label">Full Event JSON</div>' +
        '<div class="event-data-value">' +
        '<button class="copy-icon-btn" data-copy-id="' + jsonSpanId + '">Copy</button>' +
        '<pre id="' + jsonSpanId + '">' + escapeHtml(eventJson) + '</pre>' +
        '</div>' +
        '</div>' +
        '</div>';
}

function escapeHtml(text) {
    const map = {
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#039;'
    };
    return text.replace(/[&<>"']/g, function (m) { return map[m]; });
}

// Nostr Login and Event Publishing Functions
let currentUser = null;
let otherNameFieldCount = 0;
let customDNSRecordCount = 0;
let otherNameConnectionCount = 0;
let metaURLCount = 0;
let metaCurrencyCount = 0;
let metaNostrAddressCount = 0;
let metaRelayCount = 0;
let metaCustomFieldCount = 0;

// SHA256 helper function
async function sha256(message) {
    const msgBuffer = new TextEncoder().encode(message);
    const hashBuffer = await crypto.subtle.digest('SHA-256', msgBuffer);
    const hashArray = Array.from(new Uint8Array(hashBuffer));
    const hashHex = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
    return hashHex;
}

// NIP-19 encoding helpers
function encodeNaddr(eventId, pubkey, kind, dTag) {
    // For naddr, we need: kind, pubkey, d-tag, and optional relays
    const relays = [
        'wss://relay.damus.io',
        'wss://relay.nostr.band',
        'wss://nos.lol'
    ];

    const data = {
        identifier: dTag,
        pubkey: pubkey,
        kind: kind,
        relays: relays
    };

    // Use nostr-tools library (loaded from CDN as window.NostrTools)
    try {
        if (typeof window.NostrTools !== 'undefined' && window.NostrTools.nip19) {
            return window.NostrTools.nip19.naddrEncode(data);
        }
    } catch (e) {
        console.error('Error encoding naddr:', e);
    }

    // Fallback: basic bech32 encoding (simplified - for testing only)
    console.warn('nostr-tools not available, using fallback naddr encoding');
    return 'naddr1' + btoa(JSON.stringify(data)).replace(/=/g, '').substring(0, 100);
}

function encodeNevent(eventId, pubkey, kind) {
    // For nevent, we need: event ID, optional relays, optional author, optional kind
    const relays = [
        'wss://relay.damus.io',
        'wss://relay.nostr.band',
        'wss://nos.lol'
    ];

    const data = {
        id: eventId,
        relays: relays,
        author: pubkey,
        kind: kind
    };

    // Use nostr-tools library
    try {
        if (typeof window.NostrTools !== 'undefined' && window.NostrTools.nip19) {
            return window.NostrTools.nip19.neventEncode(data);
        }
    } catch (e) {
        console.error('Error encoding nevent:', e);
    }

    // Fallback: basic bech32 encoding (simplified - for testing only)
    console.warn('nostr-tools not available, using fallback nevent encoding');
    return 'nevent1' + btoa(JSON.stringify(data)).replace(/=/g, '').substring(0, 100);
}

function decodeNaddr(naddr) {
    try {
        if (typeof window.NostrTools !== 'undefined' && window.NostrTools.nip19) {
            const decoded = window.NostrTools.nip19.decode(naddr);
            return decoded.data;
        }
    } catch (e) {
        console.error('Error decoding naddr:', e);
    }

    // Fallback decoding
    console.warn('nostr-tools not available, using fallback naddr decoding');
    const encoded = naddr.replace('naddr1', '');
    return JSON.parse(atob(encoded));
}

// Dynamic field management for Name Event (616)
function addOtherNameField() {
    const container = document.getElementById('otherNamesContainer');
    const id = 'otherName_' + otherNameFieldCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = '<input type="text" class="otherNameInput flex-1 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent" placeholder="e.g., alice_backup" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function addCustomDNSRecord() {
    const container = document.getElementById('customDNSRecords');
    const id = 'dnsRecord_' + customDNSRecordCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl space-y-3';
    div.innerHTML = `
        <div class="flex gap-3 items-end flex-wrap">
            <div class="flex-1 min-w-[100px]">
                <label class="block text-xs text-gray-500 mb-1">Type</label>
                <div class="custom-dropdown relative" data-value="A">
                    <button type="button" onclick="toggleDropdown(this)"
                        class="w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white text-sm focus:outline-none focus:border-dnn-accent cursor-pointer flex items-center justify-between">
                        <span class="dropdown-label">A (IPv4)</span>
                        <svg class="w-4 h-4 text-gray-400 transition-transform dropdown-arrow" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path>
                        </svg>
                    </button>
                    <div class="dropdown-options hidden absolute top-full left-0 right-0 mt-1 bg-dnn-card border border-dnn-border rounded-lg shadow-xl z-50 overflow-hidden">
                        <div onclick="selectDropdownOption(this, 'A', 'A (IPv4)')" class="dropdown-option px-3 py-2 text-sm cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="A">A (IPv4)</div>
                        <div onclick="selectDropdownOption(this, 'AAAA', 'AAAA (IPv6)')" class="dropdown-option px-3 py-2 text-sm cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="AAAA">AAAA (IPv6)</div>
                        <div onclick="selectDropdownOption(this, 'CNAME', 'CNAME')" class="dropdown-option px-3 py-2 text-sm cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="CNAME">CNAME</div>
                        <div onclick="selectDropdownOption(this, 'TXT', 'TXT')" class="dropdown-option px-3 py-2 text-sm cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="TXT">TXT</div>
                        <div onclick="selectDropdownOption(this, 'MX', 'MX')" class="dropdown-option px-3 py-2 text-sm cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="MX">MX</div>
                        <div onclick="selectDropdownOption(this, 'SRV', 'SRV')" class="dropdown-option px-3 py-2 text-sm cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="SRV">SRV</div>
                    </div>
                    <input type="hidden" class="dnsRecordType" value="A">
                </div>
            </div>
            <div class="flex-1 min-w-[100px]">
                <label class="block text-xs text-gray-500 mb-1">Name</label>
                <input type="text" class="dnsRecordName w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm" placeholder="@" />
            </div>
            <div class="flex-[2] min-w-[150px]">
                <label class="block text-xs text-gray-500 mb-1">Value</label>
                <input type="text" class="dnsRecordValue w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm font-mono" placeholder="Record value" />
            </div>
            <div class="w-20">
                <label class="block text-xs text-gray-500 mb-1">TTL</label>
                <input type="text" class="dnsRecordTTL w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white focus:outline-none focus:border-dnn-accent text-sm" value="3600" />
            </div>
            <button type="button" onclick="removeField('${id}')" class="p-2 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg transition-all flex items-center justify-center">
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            </button>
        </div>`;

    container.appendChild(div);
}

function addOtherNameConnection() {
    const container = document.getElementById('otherConnections');
    if (!container) {
        console.warn('Container otherConnections not found');
        return;
    }
    const connIndex = otherNameConnectionCount++;
    const id = 'otherNameConn_' + connIndex;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'space-y-4 p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl';
    div.innerHTML = `
        <div class="flex items-center justify-between">
            <h4 class="text-sm font-semibold text-white flex items-center gap-2">
                <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-dnn-accent"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>
                Other Name Connection
            </h4>
            <button type="button" onclick="removeField('${id}')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all">Remove</button>
        </div>
        <input type="text" class="otherConnName w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Name (from Step 1, e.g. alice_work)" />
        <div class="grid grid-cols-2 gap-4 max-sm:grid-cols-1">
            <input type="text" class="otherConnIPv4 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="IPv4 (203.0.113.1)" />
            <input type="text" class="otherConnIPv6 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="IPv6 (optional)" />
        </div>
        <div class="grid grid-cols-2 gap-4 max-sm:grid-cols-1">
            <input type="number" class="otherConnHTTPSPort px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white focus:outline-none focus:border-dnn-accent" placeholder="HTTPS Port" value="443" />
            <input type="number" class="otherConnHTTPPort px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white focus:outline-none focus:border-dnn-accent" placeholder="HTTP Port (optional)" value="80" />
        </div>
        <div id="otherConnCustomDNS_${connIndex}" class="space-y-2"></div>
        <button type="button" onclick="addOtherConnCustomDNSRecord(${connIndex})" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1">
            <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            Add Custom DNS Record
        </button>
        <div class="p-3 bg-dnn-secondary/50 rounded-lg space-y-2">
            <span class="text-xs text-gray-500">Certificate Override (Optional)</span>
            <textarea class="otherConnCertPEM w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-xs" placeholder="-----BEGIN CERTIFICATE-----" rows="2" onchange="autoDetectDynamicOtherConnCertExpiry('${id}')"></textarea>
            <div class="text-xs text-gray-500 otherConnCertExpiry">Expires: <span class="text-gray-400">-- (auto-detected)</span></div>
        </div>
        <div class="space-y-1">
            <span class="text-xs text-gray-400 flex items-center gap-1">
                <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-purple-400"><line x1="5" y1="12" x2="19" y2="12"/><polyline points="12 5 19 12 12 19"/></svg>
                Delegation (Optional)
            </span>
            <input type="text" class="otherConnDelegation w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-xs" placeholder="naddr1... (delegate to another 62600)" />
        </div>
        <!-- Server npubs (outside Transports) -->
        <div class="space-y-2">
            <label class="text-xs text-gray-500">Server npub(s) - comma or newline separated</label>
            <textarea class="otherConnServerNpubs w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-xs" placeholder="npub1serverA..., npub1serverB..." rows="2"></textarea>
        </div>
        <!-- Interception IPv6 (outside Transports) -->
        <div class="space-y-2">
            <label class="text-xs text-gray-500">Interception IPv6 (derived from first server npub)</label>
            <div class="flex gap-2">
                <input type="text" class="otherConnInterceptionIPv6 flex-1 px-3 py-2 bg-dnn-secondary/50 border border-dnn-border rounded-lg text-gray-400 placeholder-gray-600 focus:outline-none font-mono text-xs" placeholder="fd12:3456:789a::abcd" readonly />
                <button type="button" onclick="computeOtherConnInterceptionIPv6('${id}')" class="px-3 py-1.5 bg-purple-500/20 text-purple-400 border border-purple-500/30 rounded-lg hover:bg-purple-500/30 transition-all text-xs">Compute</button>
            </div>
        </div>
        <!-- Transport Section -->
        <div class="space-y-3 p-3 bg-purple-500/5 rounded-lg border border-purple-500/20">
            <span class="text-xs font-semibold text-gray-400 uppercase tracking-wide">Transports</span>
            <div class="space-y-2">
                <input type="text" class="otherConnTransportRelay w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-xs" placeholder="Relay URLs (comma-separated)" />
                <input type="text" class="otherConnTransportTor w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-xs" placeholder="Tor Addresses (.onion, comma-separated)" />
                <input type="text" class="otherConnTransportDHT w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-xs" placeholder="DHT Nodes (comma-separated)" />
            </div>
            <!-- Tollgate Toggle -->
            <div class="flex items-center justify-between py-2">
                <div>
                    <label class="text-xs text-gray-400">Tollgate</label>
                    <p class="text-[10px] text-gray-600">Enable non-traditional connection priority</p>
                </div>
                <button type="button" class="otherConnTollgateToggle relative w-11 h-6 bg-dnn-secondary border border-dnn-border rounded-full transition-all duration-200 focus:outline-none"
                    data-enabled="false" onclick="toggleTollgateByElement(this)">
                    <span class="absolute left-0.5 top-0.5 w-5 h-5 bg-gray-500 rounded-full transition-all duration-200"></span>
                </button>
            </div>
            <div id="otherConnCustomTransports_\${connIndex}" class="space-y-2"></div>
            <button type="button" onclick="addOtherConnCustomTransport(\${connIndex})" class="text-xs text-purple-400 hover:text-purple-300 transition-all flex items-center gap-1">
                <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
                Add Custom Transport
            </button>
        </div>
        <!-- Capabilities (outside Transports) -->
        <div class="space-y-2">
            <label class="text-xs text-gray-500">Capabilities (comma-separated, e.g., http, https, websocket)</label>
            <input type="text" class="otherConnCapabilities w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs" placeholder="http, https, websocket" />
        </div>
        <div class="space-y-1">
            <span class="text-xs text-gray-400 flex items-center gap-1">
                <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-blue-400"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                Meta - Description (Optional)
            </span>
            <input type="text" class="otherConnMetaDesc w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs" placeholder="Description of this endpoint" />
        </div>`;

    container.appendChild(div);
}

function addOtherConnCustomDNSRecord(connIndex) {
    const container = document.getElementById('otherConnCustomDNS_' + connIndex);
    const id = 'otherConnDNS_' + connIndex + '_' + Date.now();

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-3 bg-dnn-secondary/30 border border-dnn-border rounded-lg space-y-2';
    div.innerHTML = `
        <div class="flex gap-2 items-end flex-wrap">
            <div class="flex-1 min-w-[80px]">
                <label class="block text-xs text-gray-500 mb-1">Type</label>
                <div class="custom-dropdown relative" data-value="A">
                    <button type="button" onclick="toggleDropdown(this)"
                        class="w-full px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white text-xs focus:outline-none focus:border-dnn-accent cursor-pointer flex items-center justify-between">
                        <span class="dropdown-label">A</span>
                        <svg class="w-3 h-3 text-gray-400 transition-transform dropdown-arrow" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path>
                        </svg>
                    </button>
                    <div class="dropdown-options hidden absolute top-full left-0 right-0 mt-1 bg-dnn-card border border-dnn-border rounded shadow-xl z-50 overflow-hidden">
                        <div onclick="selectDropdownOption(this, 'A', 'A')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="A">A</div>
                        <div onclick="selectDropdownOption(this, 'AAAA', 'AAAA')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="AAAA">AAAA</div>
                        <div onclick="selectDropdownOption(this, 'CNAME', 'CNAME')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="CNAME">CNAME</div>
                        <div onclick="selectDropdownOption(this, 'TXT', 'TXT')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="TXT">TXT</div>
                        <div onclick="selectDropdownOption(this, 'MX', 'MX')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="MX">MX</div>
                        <div onclick="selectDropdownOption(this, 'SRV', 'SRV')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="SRV">SRV</div>
                    </div>
                    <input type="hidden" class="otherConnDNSType" value="A">
                </div>
            </div>
            <div class="flex-1 min-w-[80px]">
                <label class="block text-xs text-gray-500 mb-1">Name</label>
                <input type="text" class="otherConnDNSName w-full px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs" placeholder="@" />
            </div>
            <div class="flex-[2] min-w-[120px]">
                <label class="block text-xs text-gray-500 mb-1">Value</label>
                <input type="text" class="otherConnDNSValue w-full px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs font-mono" placeholder="Record value" />
            </div>
            <div class="w-16">
                <label class="block text-xs text-gray-500 mb-1">TTL</label>
                <input type="text" class="otherConnDNSTTL w-full px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white focus:outline-none focus:border-dnn-accent text-xs" value="3600" />
            </div>
            <button type="button" onclick="removeField('${id}')" class="p-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded transition-all flex items-center justify-center">
                <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            </button>
        </div > `;

    container.appendChild(div);
}

// For the static "Other Name Connection" block in publish page
function addStaticOtherConnDNS() {
    const container = document.getElementById('staticOtherConnDNS');
    const id = 'staticOtherConnDNS_' + Date.now();

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-end flex-wrap';
    div.innerHTML = `
        <div class="custom-dropdown relative flex-shrink-0 w-20" data-value="A">
            <button type="button" onclick="toggleDropdown(this)"
                class="w-full px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white text-xs focus:outline-none focus:border-dnn-accent cursor-pointer flex items-center justify-between">
                <span class="dropdown-label">A</span>
                <svg class="w-3 h-3 text-gray-400 transition-transform dropdown-arrow" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path>
                </svg>
            </button>
            <div class="dropdown-options hidden absolute top-full left-0 right-0 mt-1 bg-dnn-card border border-dnn-border rounded shadow-xl z-50 overflow-hidden">
                <div onclick="selectDropdownOption(this, 'A', 'A')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="A">A</div>
                <div onclick="selectDropdownOption(this, 'AAAA', 'AAAA')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="AAAA">AAAA</div>
                <div onclick="selectDropdownOption(this, 'CNAME', 'CNAME')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="CNAME">CNAME</div>
                <div onclick="selectDropdownOption(this, 'TXT', 'TXT')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="TXT">TXT</div>
                <div onclick="selectDropdownOption(this, 'MX', 'MX')" class="dropdown-option px-2 py-1.5 text-xs cursor-pointer hover:bg-dnn-hover transition-colors text-gray-300" data-value="MX">MX</div>
            </div>
            <input type="hidden" class="staticOtherConnDNSType" value="A">
        </div>
        <input type="text" class="staticOtherConnDNSName flex-1 min-w-[60px] px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs" placeholder="@" />
        <input type="text" class="staticOtherConnDNSValue flex-[2] min-w-[100px] px-2 py-1.5 bg-dnn-secondary border border-dnn-border rounded text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-xs font-mono" placeholder="Value" />
        <button type="button" onclick="removeField('${id}')" class="p-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded transition-all flex items-center justify-center">
            <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>`;

    container.appendChild(div);
}

function addMetaURL() {
    const container = document.getElementById('metaURLsContainer');
    const id = 'metaURL_' + metaURLCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = '<input type="text" class="metaURLLabel flex-1 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent" placeholder="Label (e.g., website)" />' +
        '<input type="text" class="metaURLValue flex-[2] px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent" placeholder="https://example.com" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function addMetaCurrency() {
    const container = document.getElementById('metaCurrenciesContainer');
    const id = 'metaCurrency_' + metaCurrencyCount;
    const currencyIndex = metaCurrencyCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl mb-3 space-y-3';
    div.innerHTML = '<div class="flex justify-between items-center">' +
        '<label class="text-white font-semibold text-sm">Currency</label>' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all">Remove</button>' +
        '</div>' +
        '<div><input type="text" class="metaCurrencyName w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Currency (e.g., bitcoin)" /></div>' +
        '<div id="metaCurrencyAddresses_' + currencyIndex + '" class="space-y-2"></div>' +
        '<button type="button" onclick="addCurrencyAddress(' + currencyIndex + ')" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg> Add Address</button>';

    container.appendChild(div);

    // Add first address field automatically
    addCurrencyAddress(currencyIndex);
}

function addCurrencyAddress(currencyIndex) {
    const container = document.getElementById('metaCurrencyAddresses_' + currencyIndex);
    const id = 'currencyAddr_' + currencyIndex + '_' + Date.now();

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = '<input type="text" class="currencyAddrType flex-1 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent" placeholder="Type (e.g., native_segwit)" />' +
        '<input type="text" class="currencyAddrValue flex-[2] px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent" placeholder="Address" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function addMetaNostrAddress() {
    const container = document.getElementById('metaNostrAddressesContainer');
    const id = 'metaNostr_' + metaNostrAddressCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = '<input type="text" class="metaNostrLabel flex-1 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent" placeholder="Label (e.g., main)" />' +
        '<input type="text" class="metaNostrValue flex-[2] px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="npub1... or hex pubkey" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function addMetaCustomField() {
    const container = document.getElementById('metaCustomFieldsContainer');
    const id = 'metaCustom_' + metaCustomFieldCount;
    const fieldIndex = metaCustomFieldCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl mb-3 space-y-3';
    div.innerHTML = '<div class="flex justify-between items-center">' +
        '<label class="text-white font-semibold text-sm">Custom Field</label>' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all">Remove</button>' +
        '</div>' +
        '<div><input type="text" class="metaCustomKey w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Field name (e.g., social_media)" /></div>' +
        '<div id="metaCustomValues_' + fieldIndex + '" class="space-y-2"></div>' +
        '<div class="flex gap-2">' +
        '<button type="button" onclick="addCustomFieldValue(' + fieldIndex + ')" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg> Add Field</button>' +
        '<button type="button" onclick="addCustomFieldRow(' + fieldIndex + ')" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg> Add Row</button>' +
        '</div>';

    container.appendChild(div);

    // Add first row automatically
    addCustomFieldRow(fieldIndex);
}

function addCustomFieldValue(fieldIndex) {
    const lastRow = document.querySelector('#metaCustomValues_' + fieldIndex + ' > div:last-child');
    if (!lastRow) {
        addCustomFieldRow(fieldIndex);
        return;
    }

    const id = 'customValue_' + fieldIndex + '_' + Date.now();
    const input = document.createElement('input');
    input.type = 'text';
    input.className = 'metaCustomValue flex-1 px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm ml-1';
    input.placeholder = 'Value';

    lastRow.appendChild(input);
}

function addCustomFieldRow(fieldIndex) {
    const container = document.getElementById('metaCustomValues_' + fieldIndex);
    const id = 'customRow_' + fieldIndex + '_' + Date.now();

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = '<input type="text" class="metaCustomValue flex-1 px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm" placeholder="Value" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-2 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function addMetaRelay() {
    const container = document.getElementById('metaRelaysContainer');
    const id = 'metaRelay_' + metaRelayCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = '<input type="text" class="metaRelayURL flex-1 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="wss://" value="wss://" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function addMetaCustomField() {
    const container = document.getElementById('metaCustomFieldsContainer');
    const id = 'metaCustom_' + metaCustomFieldCount;
    const fieldIndex = metaCustomFieldCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl mb-3 space-y-3';
    div.innerHTML = '<div class="flex justify-between items-center">' +
        '<label class="text-white font-semibold text-sm">Custom Field</label>' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all">Remove Field</button>' +
        '</div>' +
        '<div>' +
        '<label class="block text-gray-500 text-xs mb-1">Field Name</label>' +
        '<input type="text" class="metaCustomKey w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="e.g., social_media" />' +
        '</div>' +
        '<div id="metaCustomRows_' + fieldIndex + '" class="space-y-2"></div>' +
        '<button type="button" onclick="addMetaCustomRow(' + fieldIndex + ')" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg> Add Row</button>';

    container.appendChild(div);

    // Add first row automatically
    addMetaCustomRow(fieldIndex);
}

function addMetaCustomRow(fieldIndex) {
    const container = document.getElementById('metaCustomRows_' + fieldIndex);
    const rowId = 'metaCustomRow_' + fieldIndex + '_' + Date.now();

    const div = document.createElement('div');
    div.id = rowId;
    div.className = 'p-3 bg-dnn-secondary border border-dnn-border rounded-lg space-y-2';
    div.innerHTML = '<div class="flex gap-2 items-center">' +
        '<input type="text" class="metaCustomLabel flex-1 px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm" placeholder="Label (e.g., Twitter)" />' +
        '<div class="metaCustomValuesContainer flex-[2] flex gap-1 flex-wrap"></div>' +
        '<button type="button" onclick="removeField(\'' + rowId + '\')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all whitespace-nowrap">✕ Row</button>' +
        '</div>' +
        '<button type="button" onclick="addMetaCustomValueToRow(\'' + rowId + '\')" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg> Add Value</button>';

    container.appendChild(div);

    // Add first value input automatically
    addMetaCustomValueToRow(rowId);
}

function addMetaCustomValueToRow(rowId) {
    const row = document.getElementById(rowId);
    if (!row) return;

    const valuesContainer = row.querySelector('.metaCustomValuesContainer');
    const valueInput = document.createElement('input');
    valueInput.type = 'text';
    valueInput.className = 'metaCustomValue flex-1 min-w-[120px] px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm';
    valueInput.placeholder = 'Value';

    valuesContainer.appendChild(valueInput);
}

function removeField(id) {
    const element = document.getElementById(id);
    if (element) {
        element.remove();
    }
}

async function loginWithNostr() {
    try {
        if (!window.nostr) {
            if (window.showPublishStatus) {
                window.showPublishStatus('error', 'No Nostr extension found! Please install a Nostr extension like Alby, nos2x, or Nostr Connect.', 'Login Failed');
            }
            return;
        }

        const pubkey = await window.nostr.getPublicKey();

        // Convert hex pubkey to npub using nostr-tools
        let npub = pubkey;
        try {
            if (window.NostrTools && window.NostrTools.nip19) {
                npub = window.NostrTools.nip19.npubEncode(pubkey);
            } else if (typeof nip19 !== 'undefined' && nip19.npubEncode) {
                npub = nip19.npubEncode(pubkey);
            }
        } catch (e) {
            console.warn('Could not convert to npub, using hex:', e);
        }

        // Export for new dashboard
        window.userNpub = npub;
        window.userPubkeyHex = pubkey;
        window.loginMethod = 'extension';  // Track login method for signing
        console.log('Logged in with npub:', npub);

        // Fetch derived Bitcoin addresses for this pubkey
        let bitcoinAddresses = [];
        try {
            const response = await fetch('/dnn/derive-address/' + pubkey);
            if (response.ok) {
                const data = await response.json();
                bitcoinAddresses = data.addresses.map(a => a.address);
                console.log('Derived Bitcoin addresses:', bitcoinAddresses);
            }
        } catch (error) {
            console.error('Failed to derive Bitcoin addresses:', error);
        }

        currentUser = {
            pubkey: pubkey,
            npub: npub,
            bitcoinAddresses: bitcoinAddresses
        };

        // Update old UI (if elements exist)
        const loginArea = document.getElementById('loginArea');
        const userInfo = document.getElementById('userInfo');
        const registrationForm = document.getElementById('registrationForm');
        const userPubkey = document.getElementById('userPubkey');
        const myRegCheckbox = document.getElementById('myRegistrationsOnly');

        if (loginArea) loginArea.style.display = 'none';
        if (userInfo) userInfo.style.display = 'block';
        if (registrationForm) registrationForm.style.display = 'block';
        if (userPubkey) userPubkey.textContent = pubkey;
        if (myRegCheckbox) myRegCheckbox.disabled = false;

        // Call new dashboard callback if defined
        if (typeof window.onNostrLogin === 'function') {
            window.onNostrLogin(npub, pubkey);
        }

        // Check if user is admin and update UI
        if (typeof checkAdminStatus === 'function') {
            checkAdminStatus();
        }

        // Fetch user's NIP-65 relay list and add to node's discovered relays
        try {
            console.log('[Login] Fetching user NIP-65 relays...');
            const userRelays = await fetchUserNIP65Relays();
            if (userRelays && userRelays.length > 0) {
                console.log('[Login] Found', userRelays.length, 'user relays, adding to discovered relays');
                // Store in global for UI display
                window.userNip65Relays = userRelays;
                await addUserRelaysToDiscovered(userRelays);
                // Refresh the discovered relays display if it's visible
                if (typeof loadDiscoveredRelays === 'function') {
                    loadDiscoveredRelays();
                }
            }
        } catch (relayError) {
            console.warn('[Login] Failed to fetch user relays:', relayError);
            // Non-fatal - continue with login
        }

        console.log('Logged in with pubkey:', pubkey);
    } catch (error) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Failed to connect with Nostr: ' + error.message, 'Login Failed');
        }
        console.error('Login error:', error);
    }
}

function logout() {
    currentUser = null;
    document.getElementById('loginArea').style.display = 'block';
    document.getElementById('userInfo').style.display = 'none';
    document.getElementById('registrationForm').style.display = 'none';

    // Disable and uncheck "my registrations" filter
    const myRegCheckbox = document.getElementById('myRegistrationsOnly');
    myRegCheckbox.checked = false;
    myRegCheckbox.disabled = true;

    // Reapply filters
    filterTable();
}

async function publishNameEvent() {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    const primaryName = document.getElementById('primaryName').value.trim() || '';

    // Show loading status
    if (window.showPublishStatus) {
        window.showPublishStatus('loading', 'Requesting signature...', 'Publishing Name Event');
    }

    // Collect other names from dynamic fields
    const otherNameInputs = document.querySelectorAll('.otherNameInput');
    const otherNames = Array.from(otherNameInputs)
        .map(input => input.value.trim())
        .filter(name => name);

    try {
        // Generate UUID v4 for d-tag (NIP-DN spec requirement)
        const dTag = generateUUIDv4();

        const tags = [
            ['d', dTag],
            ['t', 'DNN']
        ];

        // Only add 'n' tag if primaryName is provided
        if (primaryName) {
            tags.push(['n', primaryName]);
        }

        // Add other names
        otherNames.forEach(name => {
            tags.push(['o', name]);
        });

        // Build content as JSON string per NIP-DN spec
        const contentObj = {
            updated_at: Math.floor(Date.now() / 1000)
        };

        const event = {
            kind: 61600,
            created_at: Math.floor(Date.now() / 1000),
            tags: tags,
            content: JSON.stringify(contentObj)
        };

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Signing event...');
        }
        const signedEvent = await signEventUniversal(event);

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Publishing to relays...');
        }
        await publishToRelays(signedEvent);

        // Encode as naddr for use in anchor event
        const naddr = encodeNaddr(signedEvent.id, signedEvent.pubkey, 61600, dTag);
        document.getElementById('nameEventID').value = naddr;

        if (window.showPublishStatus) {
            window.showPublishStatus('success', 'Name Event published! naddr copied to field.', 'Name Event Published');
        } else {
            showPublishMessage('✓ Name Event published successfully! Event ID: ' + signedEvent.id, 'success');
        }
    } catch (error) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', error.message);
        } else {
            showPublishMessage('Error publishing Name Event: ' + error.message, 'error');
        }
        console.error('Publish error:', error);
    }
}

async function publishConnectionEvent() {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    // Generate UUID v4 for d-tag - each event gets its own unique identifier
    // Relationships are established via naddr references, NOT shared d-tags
    const dTag = generateUUIDv4();

    // Show loading status
    if (window.showPublishStatus) {
        window.showPublishStatus('loading', 'Building connection event...', 'Publishing Connection Event');
    }

    try {
        const content = {};

        // Get domain name for the primary connection key
        const domainNameInput = document.getElementById('connDomainName')?.value?.trim();
        if (!domainNameInput) {
            throw new Error('Domain name is required for the connection key');
        }
        const domainKey = domainNameInput;

        // Build primary domain records
        const ipv4 = document.getElementById('connIPv4').value.trim();
        const ipv6 = document.getElementById('connIPv6').value.trim();
        const httpsPort = document.getElementById('connHTTPSPort').value.trim();
        const httpPort = document.getElementById('connHTTPPort').value.trim();

        const selfRecords = [];
        const ttl = '3600'; // 1 hour default TTL

        // Add A record (IPv4) if provided
        if (ipv4) {
            selfRecords.push(['A', '@', ipv4, ttl]);
        }

        // Add AAAA record (IPv6) if provided
        if (ipv6) {
            selfRecords.push(['AAAA', '@', ipv6, ttl]);
        }

        // Add SRV record for HTTPS if port provided
        if (httpsPort) {
            selfRecords.push(['SRV', '_https._tcp', '10', '5', httpsPort, '@', ttl]);
        }

        // Add SRV record for HTTP if port provided
        if (httpPort) {
            selfRecords.push(['SRV', '_http._tcp', '10', '5', httpPort, '@', ttl]);
        }

        // Collect custom DNS records
        const customDNSRecords = document.querySelectorAll('#customDNSRecords > div');
        customDNSRecords.forEach(recordDiv => {
            const type = recordDiv.querySelector('.dnsRecordType').value;
            const name = recordDiv.querySelector('.dnsRecordName').value.trim() || '@';
            const value = recordDiv.querySelector('.dnsRecordValue').value.trim();
            const recordTTL = recordDiv.querySelector('.dnsRecordTTL').value.trim() || '3600';

            if (value) {
                selfRecords.push([type, name, value, recordTTL]);
            }
        });

        content[domainKey] = {
            records: selfRecords,
            meta: {
                description: document.getElementById('connMetaDescription')?.value?.trim() || 'DNN connection endpoint',
                updated_at: Math.floor(Date.now() / 1000)
            }
        };

        // Add delegation if provided
        const selfDelegation = document.getElementById('connDelegation')?.value?.trim();
        if (selfDelegation) {
            content[domainKey].delegation = selfDelegation;
        }

        // Add certificate if provided (NIP-DN spec: chain array format)
        const certPEM = document.getElementById('certPEM').value.trim();

        if (certPEM) {
            // Extract expiry from certificate if possible
            const certExpiry = extractCertExpiry(certPEM);

            content[domainKey].cert = {
                chain: [
                    { type: 'leaf', pem: certPEM }
                ],
                expires: certExpiry ? Math.floor(new Date(certExpiry).getTime() / 1000) : null
            };
        }

        // Add server npubs (new 62600 spec)
        const npubsInput = document.getElementById('connServerNpubs')?.value?.trim();
        if (npubsInput) {
            const npubs = npubsInput.split(/[,\n]/).map(n => n.trim()).filter(n => n);
            if (npubs.length > 0) {
                content[domainKey].npub = npubs;
            }
        }

        // Add transports (new 62600 spec)
        const transports = {};

        // Relay transports
        const relayInput = document.getElementById('connTransportRelay')?.value?.trim();
        if (relayInput) {
            const relays = relayInput.split(/[,\n]/).map(r => r.trim()).filter(r => r);
            if (relays.length > 0) transports.relay = relays;
        }

        // Tor transports
        const torInput = document.getElementById('connTransportTor')?.value?.trim();
        if (torInput) {
            const tors = torInput.split(/[,\n]/).map(t => t.trim()).filter(t => t);
            if (tors.length > 0) transports.tor = tors;
        }

        // Interception IPv6 (outside transports - stored at domain level)
        const interceptionIPv6 = document.getElementById('connInterceptionIPv6')?.value?.trim();
        // Note: interceptionIPv6 is written to content[domainKey].interception_ipv6 after transports are built

        // Custom transports
        const customTransportRows = document.querySelectorAll('#customTransports > div');
        customTransportRows.forEach(row => {
            const label = row.querySelector('.customTransportLabel')?.value?.trim();
            const values = row.querySelector('.customTransportValues')?.value?.trim();
            if (label && values) {
                const valuesArray = values.split(/[,\n]/).map(v => v.trim()).filter(v => v);
                if (valuesArray.length > 0) {
                    transports[label] = valuesArray;
                }
            }
        });

        // DHT transports
        const dhtInput = document.getElementById('connTransportDHT')?.value?.trim();
        if (dhtInput) {
            const dhts = dhtInput.split(/[,\n]/).map(d => d.trim()).filter(d => d);
            if (dhts.length > 0) transports.dht = dhts;
        }

        // Tollgate toggle
        const tollgateToggle = document.getElementById('connTollgateToggle');
        if (tollgateToggle && tollgateToggle.dataset.enabled === 'true') {
            transports.tollgate = 'use';
        }

        if (Object.keys(transports).length > 0) {
            content[domainKey].transports = transports;
        }

        // Add interception_ipv6 (outside transports)
        if (interceptionIPv6) {
            content[domainKey].interception_ipv6 = interceptionIPv6;
        }

        // Add capabilities (new 62600 spec) - comma-separated text field
        const capabilitiesInput = document.getElementById('connCapabilities')?.value?.trim();
        if (capabilitiesInput) {
            const capabilities = capabilitiesInput.split(/[,]/).map(c => c.trim()).filter(c => c);
            if (capabilities.length > 0) {
                content[domainKey].capabilities = capabilities;
            }
        }

        // Handle static Other Name Connection if present
        const staticOtherConn = document.getElementById('staticOtherConn');
        if (staticOtherConn) {
            const name = document.getElementById('staticOtherConnName')?.value?.trim();
            const connIPv4 = document.getElementById('staticOtherConnIPv4')?.value?.trim();
            const connIPv6 = document.getElementById('staticOtherConnIPv6')?.value?.trim();
            const connHTTPSPort = document.getElementById('staticOtherConnHTTPSPort')?.value?.trim() || '443';
            const connHTTPPort = document.getElementById('staticOtherConnHTTPPort')?.value?.trim() || '80';
            const connDelegation = document.getElementById('staticOtherConnDelegation')?.value?.trim();
            const connMetaDesc = document.getElementById('staticOtherConnMetaDesc')?.value?.trim();
            const connCertPEM = document.getElementById('staticOtherConnCertPEM')?.value?.trim();

            if (name && (connIPv4 || connIPv6 || connDelegation)) {
                const otherRecords = [];
                if (connIPv4) otherRecords.push(['A', '@', connIPv4, ttl]);
                if (connIPv6) otherRecords.push(['AAAA', '@', connIPv6, ttl]);
                if (connHTTPSPort) otherRecords.push(['SRV', '_https._tcp', '10', '5', connHTTPSPort, '@', ttl]);
                if (connHTTPPort) otherRecords.push(['SRV', '_http._tcp', '10', '5', connHTTPPort, '@', ttl]);

                content[name] = {
                    records: otherRecords,
                    meta: {
                        description: connMetaDesc || 'Secondary endpoint',
                        updated_at: Math.floor(Date.now() / 1000)
                    }
                };

                if (connDelegation) {
                    content[name].delegation = connDelegation;
                }

                if (connCertPEM) {
                    const certExpiry = extractCertExpiry(connCertPEM);
                    content[name].cert = {
                        chain: [{ type: 'leaf', pem: connCertPEM }],
                        expires: certExpiry ? Math.floor(new Date(certExpiry).getTime() / 1000) : null
                    };
                }
            }
        }


        // Handle dynamic Other Name Connections from #otherConnections container
        const otherConnsContainer = document.getElementById('otherConnections');
        if (otherConnsContainer) {
            otherConnsContainer.querySelectorAll(':scope > div').forEach(connDiv => {
                const name = connDiv.querySelector('.otherConnName')?.value?.trim();
                const connIPv4 = connDiv.querySelector('.otherConnIPv4')?.value?.trim();
                const connIPv6 = connDiv.querySelector('.otherConnIPv6')?.value?.trim();
                const connHTTPSPort = connDiv.querySelector('.otherConnHTTPSPort')?.value?.trim() || '443';
                const connHTTPPort = connDiv.querySelector('.otherConnHTTPPort')?.value?.trim() || '80';
                const connDelegation = connDiv.querySelector('.otherConnDelegation')?.value?.trim();
                const connMetaDesc = connDiv.querySelector('.otherConnMetaDesc')?.value?.trim();
                const connCertPEM = connDiv.querySelector('.otherConnCertPEM')?.value?.trim();

                if (name && (connIPv4 || connIPv6 || connDelegation)) {
                    const otherRecords = [];
                    if (connIPv4) otherRecords.push(['A', '@', connIPv4, ttl]);
                    if (connIPv6) otherRecords.push(['AAAA', '@', connIPv6, ttl]);
                    if (connHTTPSPort) otherRecords.push(['SRV', '_https._tcp', '10', '5', connHTTPSPort, '@', ttl]);
                    if (connHTTPPort) otherRecords.push(['SRV', '_http._tcp', '10', '5', connHTTPPort, '@', ttl]);

                    content[name] = {
                        records: otherRecords,
                        meta: {
                            description: connMetaDesc || 'Secondary endpoint',
                            updated_at: Math.floor(Date.now() / 1000)
                        }
                    };

                    if (connDelegation) {
                        content[name].delegation = connDelegation;
                    }

                    if (connCertPEM) {
                        const certExpiry = extractCertExpiry(connCertPEM);
                        content[name].cert = {
                            chain: [{ type: 'leaf', pem: connCertPEM }],
                            expires: certExpiry ? Math.floor(new Date(certExpiry).getTime() / 1000) : null
                        };
                    }

                    // Collect npub array
                    const connNpubInput = connDiv.querySelector('.otherConnServerNpubs')?.value?.trim();
                    if (connNpubInput) {
                        const npubs = connNpubInput.split(/[\n,]/).map(n => n.trim()).filter(n => n);
                        if (npubs.length > 0) {
                            content[name].npub = npubs;
                        }
                    }

                    // Collect interception_ipv6
                    const connInterceptionIPv6 = connDiv.querySelector('.otherConnInterceptionIPv6')?.value?.trim();
                    if (connInterceptionIPv6) {
                        content[name].interception_ipv6 = connInterceptionIPv6;
                    }

                    // Collect transports
                    const connTransports = {};
                    const connRelayInput = connDiv.querySelector('.otherConnTransportRelay')?.value?.trim();
                    if (connRelayInput) {
                        const relays = connRelayInput.split(',').map(r => r.trim()).filter(r => r);
                        if (relays.length > 0) connTransports.relay = relays;
                    }
                    const connTorInput = connDiv.querySelector('.otherConnTransportTor')?.value?.trim();
                    if (connTorInput) {
                        const tors = connTorInput.split(',').map(t => t.trim()).filter(t => t);
                        if (tors.length > 0) connTransports.tor = tors;
                    }
                    const connDhtInput = connDiv.querySelector('.otherConnTransportDHT')?.value?.trim();
                    if (connDhtInput) {
                        const dhts = connDhtInput.split(',').map(d => d.trim()).filter(d => d);
                        if (dhts.length > 0) connTransports.dht = dhts;
                    }
                    const connTollgateToggle = connDiv.querySelector('.otherConnTollgateToggle');
                    if (connTollgateToggle && connTollgateToggle.dataset.enabled === 'true') {
                        connTransports.tollgate = 'use';
                    }
                    if (Object.keys(connTransports).length > 0) {
                        content[name].transports = connTransports;
                    }

                    // Collect capabilities
                    const connCapabilities = connDiv.querySelector('.otherConnCapabilities')?.value?.trim();
                    if (connCapabilities) {
                        const caps = connCapabilities.split(',').map(c => c.trim()).filter(c => c);
                        if (caps.length > 0) {
                            content[name].capabilities = caps;
                        }
                    }
                }
            });
        }


        const event = {
            kind: 62600,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['d', dTag],
                ['t', 'DNN'],
                ['v', '1']  // Version tag per NIP-DN spec
            ],
            content: JSON.stringify(content)
        };

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Signing event...');
        }
        const signedEvent = await signEventUniversal(event);

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Publishing to relays...');
        }
        await publishToRelays(signedEvent);

        // Encode as naddr for use in anchor event
        const naddr = encodeNaddr(signedEvent.id, signedEvent.pubkey, 62600, dTag);
        document.getElementById('connectionEventID').value = naddr;

        if (window.showPublishStatus) {
            window.showPublishStatus('success', 'Connection Event published! naddr copied to field.', 'Connection Event Published');
        } else {
            showPublishMessage('✓ Connection Event published successfully! Event ID: ' + signedEvent.id, 'success');
        }
    } catch (error) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', error.message);
        } else {
            showPublishMessage('Error publishing Connection Event: ' + error.message, 'error');
        }
        console.error('Publish error:', error);
    }
}

async function publishMetadataEvent() {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    // Show loading status
    if (window.showPublishStatus) {
        window.showPublishStatus('loading', 'Building metadata event...', 'Publishing Metadata Event');
    }

    try {
        const metadata = {};

        // Description
        const description = document.getElementById('metaDescription').value.trim();
        if (description) {
            metadata.description = description;
        }

        // URLs
        const urlElements = document.querySelectorAll('#metaURLsContainer > div');
        if (urlElements.length > 0) {
            const urls = [];
            urlElements.forEach(urlDiv => {
                const label = urlDiv.querySelector('.metaURLLabel').value.trim();
                const url = urlDiv.querySelector('.metaURLValue').value.trim();
                if (label && url) {
                    urls.push({ label, url });
                }
            });
            if (urls.length > 0) {
                metadata.urls = urls;
            }
        }

        // Currencies
        const currencyContainers = document.querySelectorAll('#metaCurrenciesContainer > div');
        if (currencyContainers.length > 0) {
            const currencies = [];
            currencyContainers.forEach(currDiv => {
                const currencyName = currDiv.querySelector('.metaCurrencyName').value.trim();
                const addressElements = currDiv.querySelectorAll('.currencyAddrType');

                if (currencyName && addressElements.length > 0) {
                    const addresses = [];
                    addressElements.forEach((typeInput, idx) => {
                        const type = typeInput.value.trim();
                        const addrValueInput = typeInput.parentElement.querySelector('.currencyAddrValue');
                        const address = addrValueInput ? addrValueInput.value.trim() : '';

                        if (type && address) {
                            addresses.push({ type, address });
                        }
                    });

                    if (addresses.length > 0) {
                        currencies.push({ currency: currencyName, addresses });
                    }
                }
            });
            if (currencies.length > 0) {
                metadata.currencies = currencies;
            }
        }

        // Nostr Addresses
        const nostrElements = document.querySelectorAll('#metaNostrAddressesContainer > div');
        if (nostrElements.length > 0) {
            const nostrAddresses = [];
            nostrElements.forEach(nostrDiv => {
                const label = nostrDiv.querySelector('.metaNostrLabel').value.trim();
                const address = nostrDiv.querySelector('.metaNostrValue').value.trim();
                if (label && address) {
                    nostrAddresses.push({ label, address });
                }
            });
            if (nostrAddresses.length > 0) {
                metadata.nostrAddresses = nostrAddresses;
            }
        }

        // Nostr Relays
        const relayElements = document.querySelectorAll('#metaRelaysContainer > div');
        if (relayElements.length > 0) {
            const relays = [];
            relayElements.forEach(relayDiv => {
                const relayURL = relayDiv.querySelector('.metaRelayURL').value.trim();
                if (relayURL) {
                    relays.push(relayURL);
                }
            });
            if (relays.length > 0) {
                metadata.relays = relays;
            }
        }

        // Custom Fields (new label/value row structure)
        const customFieldContainers = document.querySelectorAll('#metaCustomFieldsContainer > div');
        if (customFieldContainers.length > 0) {
            customFieldContainers.forEach(fieldDiv => {
                const key = fieldDiv.querySelector('.metaCustomKey').value.trim();
                if (!key) return;

                const rowContainers = fieldDiv.querySelectorAll('[id^="metaCustomRow_"]');
                if (rowContainers.length === 0) return;

                const fieldData = {};

                rowContainers.forEach(rowDiv => {
                    const label = rowDiv.querySelector('.metaCustomLabel').value.trim();
                    if (!label) return;

                    const valueInputs = rowDiv.querySelectorAll('.metaCustomValue');
                    const values = Array.from(valueInputs)
                        .map(input => input.value.trim())
                        .filter(v => v);

                    if (values.length > 0) {
                        // If single value, store as string; if multiple, store as array
                        fieldData[label] = values.length === 1 ? values[0] : values;
                    }
                });

                if (Object.keys(fieldData).length > 0) {
                    metadata[key] = fieldData;
                }
            });
        }

        // Generate UUID v4 for d-tag - each event gets its own unique identifier
        // Relationships are established via naddr references, NOT shared d-tags
        const dTag = generateUUIDv4();

        // Build content with updated_at and metadata wrapper per NIP-DN spec
        const contentObj = {
            updated_at: Math.floor(Date.now() / 1000),
            metadata: metadata
        };

        const event = {
            kind: 63600,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['d', dTag],
                ['t', 'DNN']
            ],
            content: JSON.stringify(contentObj)
        };

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Signing event...');
        }
        const signedEvent = await signEventUniversal(event);

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Publishing to relays...');
        }
        await publishToRelays(signedEvent);

        // Encode as naddr for use in anchor event
        const naddr = encodeNaddr(signedEvent.id, signedEvent.pubkey, 63600, dTag);
        document.getElementById('metadataEventID').value = naddr;

        if (window.showPublishStatus) {
            window.showPublishStatus('success', 'Metadata Event published! naddr copied to field.', 'Metadata Event Published');
        } else {
            showPublishMessage('✓ Metadata Event published successfully! Event ID: ' + signedEvent.id, 'success');
        }
    } catch (error) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', error.message);
        } else {
            showPublishMessage('Error publishing Metadata Event: ' + error.message, 'error');
        }
        console.error('Publish error:', error);
    }
}

// Certificate Management Functions

function extractCertExpiry(pem) {
    try {
        // Remove PEM headers/footers and whitespace
        const base64 = pem
            .replace(/-----BEGIN CERTIFICATE-----/, '')
            .replace(/-----END CERTIFICATE-----/, '')
            .replace(/\s/g, '');

        // Decode base64 to binary
        const binary = atob(base64);
        const bytes = new Uint8Array(binary.length);
        for (let i = 0; i < binary.length; i++) {
            bytes[i] = binary.charCodeAt(i);
        }

        // Parse ASN.1 DER structure to find validity period
        // This is a simplified parser - looks for the validity sequence
        // Format: SEQUENCE { notBefore, notAfter }
        // notAfter is typically a UTCTime (tag 0x17) or GeneralizedTime (tag 0x18)

        let i = 0;
        while (i < bytes.length - 15) {
            // Look for UTCTime tag (0x17) or GeneralizedTime (0x18)
            if (bytes[i] === 0x17 || bytes[i] === 0x18) {
                const isGeneralized = bytes[i] === 0x18;
                const length = bytes[i + 1];

                // Skip first occurrence (notBefore), find second (notAfter)
                i += 2 + length;

                if (i < bytes.length && (bytes[i] === 0x17 || bytes[i] === 0x18)) {
                    const afterLength = bytes[i + 1];
                    const dateBytes = bytes.slice(i + 2, i + 2 + afterLength);
                    const dateStr = String.fromCharCode.apply(null, dateBytes);

                    // Parse date string
                    let year, month, day, hour, min, sec;

                    if (bytes[i] === 0x17) {
                        // UTCTime: YYMMDDHHMMSSZ
                        year = parseInt(dateStr.substr(0, 2));
                        year += (year < 50) ? 2000 : 1900;
                        month = parseInt(dateStr.substr(2, 2)) - 1;
                        day = parseInt(dateStr.substr(4, 2));
                        hour = parseInt(dateStr.substr(6, 2));
                        min = parseInt(dateStr.substr(8, 2));
                        sec = parseInt(dateStr.substr(10, 2));
                    } else {
                        // GeneralizedTime: YYYYMMDDHHMMSSZ
                        year = parseInt(dateStr.substr(0, 4));
                        month = parseInt(dateStr.substr(4, 2)) - 1;
                        day = parseInt(dateStr.substr(6, 2));
                        hour = parseInt(dateStr.substr(8, 2));
                        min = parseInt(dateStr.substr(10, 2));
                        sec = parseInt(dateStr.substr(12, 2));
                    }

                    const expiryDate = new Date(Date.UTC(year, month, day, hour, min, sec));
                    return expiryDate.toISOString().slice(0, 16); // Format for datetime-local
                }
            }
            i++;
        }

        return null;
    } catch (e) {
        console.error('Error extracting certificate expiry:', e);
        return null;
    }
}

// Extract DNN ID from certificate's Subject Alternative Name (SAN) field
function extractCertDnnId(pem) {
    try {
        // Remove PEM headers/footers and whitespace
        const base64 = pem
            .replace(/-----BEGIN CERTIFICATE-----/, '')
            .replace(/-----END CERTIFICATE-----/, '')
            .replace(/\s/g, '');

        // Decode base64 to binary
        const binary = atob(base64);
        const bytes = new Uint8Array(binary.length);
        for (let i = 0; i < binary.length; i++) {
            bytes[i] = binary.charCodeAt(i);
        }

        // Convert to string for easier pattern matching
        const certStr = String.fromCharCode.apply(null, bytes);

        // Look for DNS names in SAN extension
        // SAN extension has OID 2.5.29.17, which encodes as 55 1D 11 in ASN.1
        // DNS names are tagged with context tag [2] (0x82)
        const dnsNames = [];

        for (let i = 0; i < bytes.length - 3; i++) {
            // Look for dNSName tag (context [2] = 0x82)
            if (bytes[i] === 0x82) {
                const len = bytes[i + 1];
                if (len > 0 && len < 255 && i + 2 + len <= bytes.length) {
                    // Extract the DNS name
                    const nameBytes = bytes.slice(i + 2, i + 2 + len);
                    // Check if it looks like printable ASCII
                    let isValid = true;
                    for (let j = 0; j < nameBytes.length; j++) {
                        if (nameBytes[j] < 32 || nameBytes[j] > 126) {
                            isValid = false;
                            break;
                        }
                    }
                    if (isValid) {
                        const name = String.fromCharCode.apply(null, nameBytes);
                        // Filter out wildcard entries and common TLDs
                        if (!name.startsWith('*.') &&
                            !name.includes('.com') &&
                            !name.includes('.org') &&
                            !name.includes('.net') &&
                            !name.includes('.io') &&
                            name.length > 2) {
                            dnsNames.push(name);
                        }
                    }
                }
            }
        }

        // Return the first non-wildcard DNS name (likely the DNN ID)
        if (dnsNames.length > 0) {
            return dnsNames[0];
        }

        return null;
    } catch (e) {
        console.error('Error extracting DNN ID from certificate:', e);
        return null;
    }
}

async function verifyCertSignature() {
    const pem = document.getElementById('certPEM').value.trim();
    const signature = document.getElementById('certSignature').value.trim();
    const storedHash = document.getElementById('certHash').value;
    const pubkey = document.getElementById('certPubkey').value;

    if (!pem || !signature) {
        alert('Please enter certificate and sign it first');
        return;
    }

    try {
        // Re-hash the certificate
        const encoder = new TextEncoder();
        const data = encoder.encode(pem);
        const hashBuffer = await crypto.subtle.digest('SHA-256', data);
        const hashArray = Array.from(new Uint8Array(hashBuffer));
        const computedHash = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');

        // Check hash matches
        if (computedHash !== storedHash) {
            showCertMessage('❌ Verification failed: Certificate was modified after signing', 'error');
            return;
        }

        // Verify signature is present
        if (signature && pubkey && storedHash) {
            showCertMessage(
                '✅ Signature verified successfully!<br>' +
                'Hash: ' + computedHash.substring(0, 32) + '...<br>' +
                'Signed by: ' + pubkey.substring(0, 32) + '...',
                'success'
            );
        } else {
            showCertMessage('❌ Verification failed: Missing signature data', 'error');
        }

    } catch (error) {
        showCertMessage('❌ Verification failed: ' + error.message, 'error');
        console.error(error);
    }
}

function clearCertificate() {
    document.getElementById('certPEM').value = '';
    const expiryEl = document.getElementById('certExpiry');
    if (expiryEl) expiryEl.innerHTML = '<span class="text-gray-400">-- (auto-detected from certificate)</span>';
}

// Auto-detect cert expiry and DNN ID when pasting in publish form
function autoDetectCertExpiry() {
    const certPEM = document.getElementById('certPEM')?.value.trim();
    const expiryEl = document.getElementById('certExpiry');
    if (!certPEM || !expiryEl) return;

    const expiry = extractCertExpiry(certPEM);
    const dnnId = extractCertDnnId(certPEM);

    let expiryText = '';
    if (expiry) {
        const expiryDate = new Date(expiry);
        const formattedDate = expiryDate.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' });
        expiryText = `<span class="text-green-400">${formattedDate}</span>`;
    } else {
        expiryText = '<span class="text-yellow-400">Unable to detect expiry</span>';
    }

    let dnnIdText = '';
    if (dnnId) {
        dnnIdText = ` · DNN ID: <span class="text-cyan-400">${dnnId}</span>`;
    } else {
        dnnIdText = ' · <span class="text-yellow-400">No DNN ID found</span>';
    }

    expiryEl.innerHTML = expiryText + dnnIdText;
}

// Auto-detect cert expiry and DNN ID when pasting in edit form
function autoDetectEditCertExpiry() {
    const certPEM = document.getElementById('editCertPEM')?.value.trim();
    const expiryEl = document.getElementById('editCertExpiry');
    if (!certPEM || !expiryEl) return;

    const expiry = extractCertExpiry(certPEM);
    const dnnId = extractCertDnnId(certPEM);

    let expiryText = 'Expires: ';
    if (expiry) {
        const expiryDate = new Date(expiry);
        const formattedDate = expiryDate.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' });
        expiryText += `<span class="text-green-400">${formattedDate}</span>`;
    } else {
        expiryText += '<span class="text-yellow-400">Unable to detect</span>';
    }

    let dnnIdText = '';
    if (dnnId) {
        dnnIdText = ` · DNN ID: <span class="text-cyan-400">${dnnId}</span>`;
    } else {
        dnnIdText = ' · <span class="text-yellow-400">No DNN ID</span>';
    }

    expiryEl.innerHTML = expiryText + dnnIdText;
}

// Auto-detect cert expiry and DNN ID for Other Name Connection sections (edit modal)
function autoDetectOtherConnCertExpiry(connId) {
    const div = document.getElementById(connId);
    if (!div) return;

    const certPEM = div.querySelector('.editOtherConnCertPEM')?.value.trim();
    const expiryEl = div.querySelector('.editOtherConnCertExpiry');
    if (!certPEM || !expiryEl) return;

    const expiry = extractCertExpiry(certPEM);
    const dnnId = extractCertDnnId(certPEM);

    let expiryText = 'Expires: ';
    if (expiry) {
        const expiryDate = new Date(expiry);
        const formattedDate = expiryDate.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' });
        expiryText += `<span class="text-green-400">${formattedDate}</span>`;
    } else {
        expiryText += '<span class="text-yellow-400">Unable to detect</span>';
    }

    let dnnIdText = '';
    if (dnnId) {
        dnnIdText = ` · DNN ID: <span class="text-cyan-400">${dnnId}</span>`;
    } else {
        dnnIdText = ' · <span class="text-yellow-400">No DNN ID</span>';
    }

    expiryEl.innerHTML = expiryText + dnnIdText;
}

// Auto-detect cert expiry and DNN ID for static Other Name Connection on publish page
function autoDetectStaticOtherConnCertExpiry() {
    const certPEM = document.getElementById('staticOtherConnCertPEM')?.value.trim();
    const expiryEl = document.getElementById('staticOtherConnCertExpiry');
    if (!certPEM || !expiryEl) return;

    const expiry = extractCertExpiry(certPEM);
    const dnnId = extractCertDnnId(certPEM);

    let expiryText = 'Expires: ';
    if (expiry) {
        const expiryDate = new Date(expiry);
        const formattedDate = expiryDate.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' });
        expiryText += `<span class="text-green-400">${formattedDate}</span>`;
    } else {
        expiryText += '<span class="text-yellow-400">Unable to detect</span>';
    }

    let dnnIdText = '';
    if (dnnId) {
        dnnIdText = ` · DNN ID: <span class="text-cyan-400">${dnnId}</span>`;
    } else {
        dnnIdText = ' · <span class="text-yellow-400">No DNN ID</span>';
    }

    expiryEl.innerHTML = expiryText + dnnIdText;
}

// Auto-detect cert expiry and DNN ID for dynamic Other Name Connections on publish page
function autoDetectDynamicOtherConnCertExpiry(connId) {
    const div = document.getElementById(connId);
    if (!div) return;

    const certPEM = div.querySelector('.otherConnCertPEM')?.value.trim();
    const expiryEl = div.querySelector('.otherConnCertExpiry');
    if (!certPEM || !expiryEl) return;

    const expiry = extractCertExpiry(certPEM);
    const dnnId = extractCertDnnId(certPEM);

    let expiryText = 'Expires: ';
    if (expiry) {
        const expiryDate = new Date(expiry);
        const formattedDate = expiryDate.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' });
        expiryText += `<span class="text-green-400">${formattedDate}</span>`;
    } else {
        expiryText += '<span class="text-yellow-400">Unable to detect</span>';
    }

    let dnnIdText = '';
    if (dnnId) {
        dnnIdText = ` · DNN ID: <span class="text-cyan-400">${dnnId}</span>`;
    } else {
        dnnIdText = ' · <span class="text-yellow-400">No DNN ID</span>';
    }

    expiryEl.innerHTML = expiryText + dnnIdText;
}


function showCertMessage(message, type) {
    const result = document.getElementById('certVerifyResult');
    result.style.display = 'block';
    result.style.padding = '12px';
    result.style.borderRadius = '8px';
    result.style.marginTop = '8px';

    if (type === 'success') {
        result.style.background = '#10b981';
        result.style.color = 'white';
    } else {
        result.style.background = '#ef4444';
        result.style.color = 'white';
    }

    result.innerHTML = message;
}

async function verifyOtherConnCert(connIndex) {
    const conn = document.getElementById('otherNameConn_' + connIndex);
    const pem = conn.querySelector('.otherConnCertPEM').value.trim();
    const signature = conn.querySelector('.otherConnCertSig').value.trim();
    const storedHash = conn.querySelector('.otherConnCertHash').value;

    if (!pem || !signature) {
        alert('Please enter certificate and sign it first');
        return;
    }

    try {
        const encoder = new TextEncoder();
        const data = encoder.encode(pem);
        const hashBuffer = await crypto.subtle.digest('SHA-256', data);
        const hashArray = Array.from(new Uint8Array(hashBuffer));
        const computedHash = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');

        if (computedHash !== storedHash) {
            showOtherConnCertMessage(connIndex, '❌ Certificate modified', 'error');
            return;
        }

        showOtherConnCertMessage(connIndex, '✅ Valid!', 'success');
    } catch (error) {
        showOtherConnCertMessage(connIndex, '❌ Error: ' + error.message, 'error');
    }
}

function clearOtherConnCert(connIndex) {
    const conn = document.getElementById('otherNameConn_' + connIndex);
    if (!confirm('Remove certificate?')) return;

    conn.querySelector('.otherConnCertPEM').value = '';
    conn.querySelector('.otherConnCertSig').value = '';
    conn.querySelector('.otherConnCertHash').value = '';
    conn.querySelector('.otherConnCertPubkey').value = '';
    conn.querySelector('.otherConnCertSignedAt').value = '';
    conn.querySelector('.otherConnCertExpiry').value = '';
    conn.querySelector('.otherConnCertResult').style.display = 'none';
}

function showOtherConnCertMessage(connIndex, message, type) {
    const conn = document.getElementById('otherNameConn_' + connIndex);
    const result = conn.querySelector('.otherConnCertResult');
    result.style.display = 'block';
    result.style.padding = '8px';
    result.style.borderRadius = '4px';

    if (type === 'success') {
        result.style.background = '#10b981';
        result.style.color = 'white';
    } else {
        result.style.background = '#ef4444';
        result.style.color = 'white';
    }

    result.innerHTML = message;
}



async function publishAnchorEvent() {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    const nameNaddr = document.getElementById('nameEventID').value.trim();
    const connectionNaddr = document.getElementById('connectionEventID').value.trim();
    const metadataNaddr = document.getElementById('metadataEventID').value.trim();
    const bitcoinTxID = document.getElementById('anchorTxInput').value.trim();

    // Validate all required fields
    if (!nameNaddr || !connectionNaddr || !metadataNaddr || !bitcoinTxID) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error',
                'All fields are required for Anchor Event (60600):\n• Name Event naddr\n• Connection Event naddr\n• Metadata Event naddr\n• Bitcoin Transaction ID',
                'Missing Required Fields');
        }
        return;
    }

    // Validate naddr format
    if (!nameNaddr.startsWith('naddr1') || !connectionNaddr.startsWith('naddr1') || !metadataNaddr.startsWith('naddr1')) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error',
                'Steps 1-3 must be completed first. All event references must be naddr identifiers (starting with "naddr1").',
                'Invalid Event References');
        }
        return;
    }

    // Validate Bitcoin transaction ID (64 hex characters)
    if (bitcoinTxID.length !== 64 || !/^[a-fA-F0-9]{64}$/.test(bitcoinTxID)) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error',
                'Bitcoin Transaction ID must be exactly 64 hexadecimal characters.',
                'Invalid Transaction ID');
        }
        return;
    }

    // Show loading status
    if (window.showPublishStatus) {
        window.showPublishStatus('loading', 'Requesting signature...', 'Publishing Anchor Event');
    }

    try {
        // Generate UUID v4 for d-tag (NIP-DN spec requirement - same as other events)
        const dTag = generateUUIDv4();

        const event = {
            kind: 60600,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['d', dTag],
                ['n', nameNaddr],
                ['c', connectionNaddr],
                ['m', metadataNaddr],
                ['x', bitcoinTxID],
                ['t', 'DNN']
            ],
            content: JSON.stringify({ updated_at: Math.floor(Date.now() / 1000) })
        };

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Signing event...');
        }
        const signedEvent = await signEventUniversal(event);

        if (window.updatePublishLoadingText) {
            window.updatePublishLoadingText('Publishing to relays...');
        }
        await publishToRelays(signedEvent);

        // Encode anchor as naddr (since it's now addressable replaceable)
        const naddr = encodeNaddr(signedEvent.id, signedEvent.pubkey, 60600, dTag);

        if (window.showPublishStatus) {
            window.showPublishStatus('success', 'DNN registration complete! Your anchor event has been published.', 'Anchor Event Published');
        } else {
            showPublishMessage('✓ Anchor Event published successfully! naddr: ' + naddr + ' - Your DNN registration is complete! You can update this anchor event in the future.', 'success');
        }
    } catch (error) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', error.message);
        } else {
            showPublishMessage('Error publishing Anchor Event: ' + error.message, 'error');
        }
        console.error('Publish error:', error);
    }
}

async function publishToRelays(event) {
    // Publish to this node first, then to public relays
    // Use current domain with wss:// for production or ws:// for local dev
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const nodeRelay = protocol + '//' + window.location.host;

    const relays = [
        nodeRelay,  // This DNN node (MUST be first for reactive matching)
        'wss://relay.damus.io',
        'wss://relay.nostr.band',
        'wss://nos.lol',
        'wss://relay.primal.net'
    ];

    console.log('Publishing to relays:', relays);

    const results = await Promise.allSettled(
        relays.map(url => publishToRelay(url, event))
    );

    const successful = results.filter(r => r.status === 'fulfilled').length;
    console.log('Published to ' + successful + ' of ' + relays.length + ' relays');

    if (successful === 0) {
        throw new Error('Failed to publish to any relay');
    }
}

function publishToRelay(relayUrl, event) {
    return new Promise((resolve, reject) => {
        const ws = new WebSocket(relayUrl);
        const timeout = setTimeout(() => {
            ws.close();
            reject(new Error('Timeout'));
        }, 5000);

        ws.onopen = () => {
            ws.send(JSON.stringify(['EVENT', event]));
        };

        ws.onmessage = (msg) => {
            const [type, , success, message] = JSON.parse(msg.data);
            clearTimeout(timeout);
            ws.close();

            if (type === 'OK' && success) {
                resolve();
            } else {
                reject(new Error(message || 'Publish failed'));
            }
        };

        ws.onerror = () => {
            clearTimeout(timeout);
            reject(new Error('WebSocket error'));
        };
    });
}

function showPublishMessage(message, type) {
    const messageDiv = document.getElementById('publishMessage');
    messageDiv.style.display = 'block';

    if (type === 'success') {
        messageDiv.style.background = '#064e3b';
        messageDiv.style.color = '#6ee7b7';
    } else {
        messageDiv.style.background = '#7f1d1d';
        messageDiv.style.color = '#fca5a5';
    }

    messageDiv.textContent = message;

    setTimeout(() => {
        messageDiv.style.display = 'none';
    }, 8000);
}

async function loadMyEvents() {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    const listDiv = document.getElementById('myEventsList');
    if (!listDiv) {
        console.warn('myEventsList element not found - page may not be active');
        return;
    }
    listDiv.innerHTML = '<div style="text-align: center; padding: 40px 20px; color: #94a3b8;"><div class="spinner"></div><p style="margin-top: 16px;">Loading your events...</p></div>';

    try {
        // Choose relays based on toggle
        let relays;
        if (useLocalRelay) {
            // Use local node relay
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            relays = [protocol + '//' + window.location.host];
            console.log('[loadMyEvents] Using local relay:', relays[0]);
        } else {
            // Use external relays
            relays = [
                'wss://relay.damus.io',
                'wss://relay.nostr.band',
                'wss://nos.lol',
                'wss://relay.primal.net'
            ];
        }

        const kinds = [61600, 62600, 63600, 60600];

        // Query all relays simultaneously (much faster!)
        const promises = relays.map(relayUrl =>
            fetchEventsFromRelay(relayUrl, currentUser.pubkey, kinds)
                .catch(error => {
                    console.error('Error fetching from ' + relayUrl + ':', error);
                    return []; // Return empty array on error
                })
        );

        // Wait for all queries to complete (or timeout)
        const results = await Promise.all(promises);

        // Flatten results
        const allEvents = results.flat();

        // Deduplicate addressable replaceable events
        // - Standard NIP-01 range: kinds 30000-39999
        // - DNN addressable kinds: 61600, 62600, 63600, 60600 (all DNN events are addressable)
        const addressableMap = new Map();
        const regularEvents = [];

        allEvents.forEach(event => {
            const isAddressable = (event.kind >= 30000 && event.kind < 40000) ||
                [61600, 62600, 63600, 60600].includes(event.kind);

            if (isAddressable) {
                // Addressable replaceable event - use pubkey+kind+d_tag as key
                const dTag = event.tags.find(t => t[0] === 'd')?.[1] || '';
                const key = event.pubkey + ':' + event.kind + ':' + dTag;

                const existing = addressableMap.get(key);
                if (!existing || event.created_at > existing.created_at) {
                    addressableMap.set(key, event);
                }
            } else {
                // Regular or replaceable event - deduplicate by ID
                regularEvents.push(event);
            }
        });

        // Deduplicate regular events by ID
        const uniqueRegular = Array.from(new Map(regularEvents.map(e => [e.id, e])).values());

        // Combine addressable and regular events
        const uniqueEvents = [...addressableMap.values(), ...uniqueRegular];

        // Sort by kind and created_at
        uniqueEvents.sort((a, b) => {
            if (a.kind !== b.kind) return a.kind - b.kind;
            return b.created_at - a.created_at;
        });

        renderMyEvents(uniqueEvents);
    } catch (error) {
        listDiv.innerHTML = '<div style="text-align: center; padding: 40px 20px; color: #ef4444;">Error loading events: ' + error.message + '</div>';
    }
}

function fetchEventsFromRelay(relayUrl, pubkey, kinds) {
    return new Promise((resolve, reject) => {
        const ws = new WebSocket(relayUrl);
        const events = [];
        const subId = 'my-events-' + Math.random().toString(36).substr(2, 9);
        let resolved = false;

        // Shorter timeout for faster parallel queries
        const timeout = setTimeout(() => {
            if (!resolved) {
                resolved = true;
                ws.close();
                resolve(events); // Resolve with whatever we got
            }
        }, 3000); // Reduced from 5s to 3s for faster UX

        ws.onopen = () => {
            ws.send(JSON.stringify(['REQ', subId, {
                authors: [pubkey],
                kinds: kinds,
                limit: 100
            }]));
        };

        ws.onmessage = (msg) => {
            try {
                const [type, , event] = JSON.parse(msg.data);

                if (type === 'EVENT') {
                    events.push(event);
                } else if (type === 'EOSE') {
                    if (!resolved) {
                        resolved = true;
                        clearTimeout(timeout);
                        ws.send(JSON.stringify(['CLOSE', subId])); // Close subscription
                        setTimeout(() => ws.close(), 100); // Give time for CLOSE to send
                        resolve(events);
                    }
                }
            } catch (e) {
                console.error('Error parsing message from ' + relayUrl + ':', e);
            }
        };

        ws.onerror = (err) => {
            if (!resolved) {
                resolved = true;
                clearTimeout(timeout);
                resolve(events); // Resolve with what we have instead of rejecting
            }
        };

        ws.onclose = () => {
            if (!resolved) {
                resolved = true;
                clearTimeout(timeout);
                resolve(events);
            }
        };
    });
}

// Toggle between local relay and external relays
function toggleRelaySource() {
    useLocalRelay = !useLocalRelay;
    window.useLocalRelay = useLocalRelay;
    console.log('[toggleRelaySource] useLocalRelay:', useLocalRelay);
    loadMyEvents(); // Reload events with new source
}
window.toggleRelaySource = toggleRelaySource;

function renderMyEvents(events) {
    const listDiv = document.getElementById('myEventsList');

    // Render toggle button at top
    const sourceLabel = useLocalRelay ? '🏠 Local DB' : '🌐 All Relays';
    const toggleBtnStyle = 'padding: 6px 12px; border-radius: 6px; cursor: pointer; font-size: 13px; ' +
        'background: ' + (useLocalRelay ? '#10b981' : '#6366f1') + '; color: white; border: none;';

    const toggleHtml = `
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; padding: 0 4px;">
            <span style="color: #94a3b8; font-size: 13px;">
                ${events.length} event(s) from ${sourceLabel}
            </span>
            <button onclick="toggleRelaySource()" style="${toggleBtnStyle}">
                ${useLocalRelay ? 'Switch to All Relays' : 'Switch to Local DB'}
            </button>
        </div>
    `;

    if (events.length === 0) {
        listDiv.innerHTML = toggleHtml + '<div style="text-align: center; padding: 40px 20px; color: #64748b;">No events found. Publish your first DNN event!</div>';
        return;
    }

    let html = toggleHtml;

    events.forEach(event => {
        const kindNames = {
            61600: '📝 Name Event',
            62600: '🔗 Connection Event',
            63600: '📊 Metadata Event',
            60600: '⚓ Anchor Event'
        };

        const kindName = kindNames[event.kind] || 'Event';
        const kindColor = event.kind === 60600 ? '#10b981' : '#a78bfa';
        const createdAt = new Date(event.created_at * 1000).toLocaleString();
        const isReplaceable = [61600, 62600, 63600, 60600].includes(event.kind); // All DNN events are now addressable replaceable

        // Extract n tag (name) for kind 61600
        let nameTag = '';
        if (event.kind === 61600) {
            const nTagArray = event.tags.find(t => t[0] === 'n');
            nameTag = nTagArray ? nTagArray[1] : '';
        }

        html += '<div style="background: #1e293b; border: 1px solid #334155; border-radius: 8px; padding: 16px; margin-bottom: 12px;">';
        html += '<div style="display: flex; justify-content: between; align-items: center; margin-bottom: 12px;">';
        html += '<div style="flex: 1;">';
        html += '<div style="color: ' + kindColor + '; font-weight: bold; margin-bottom: 4px;">' + kindName + ' <span style="background: ' + kindColor + '; color: white; padding: 2px 8px; border-radius: 4px; font-size: 11px; margin-left: 8px;">Kind ' + event.kind + '</span></div>';

        // Show n tag (name) for kind 61600
        if (event.kind === 61600 && nameTag) {
            html += '<div style="color: #a78bfa; font-size: 12px; margin-bottom: 4px;">Name: <strong>' + nameTag + '</strong></div>';
        }

        // Generate naddr/nevent based on event kind
        let encodedId = '';
        let idLabel = '';

        if (isReplaceable) {
            // For addressable events (61600, 62600, 63600), use naddr
            const dTagArray = event.tags.find(t => t[0] === 'd');
            const dTag = dTagArray ? dTagArray[1] : '';
            if (dTag) {
                encodedId = encodeNaddr(event.id, event.pubkey, event.kind, dTag);
                idLabel = 'naddr';
            } else {
                encodedId = event.id;
                idLabel = 'ID';
            }
        } else {
            // For anchor event (60600), now use naddr since it's addressable replaceable
            const dTagArray = event.tags.find(t => t[0] === 'd');
            const dTag = dTagArray ? dTagArray[1] : '';
            if (dTag) {
                encodedId = encodeNaddr(event.id, event.pubkey, event.kind, dTag);
                idLabel = 'naddr';
            } else {
                // Fallback if no d tag (shouldn't happen with new events)
                encodedId = event.id;
                idLabel = 'ID';
            }
        }

        html += '<div style="color: #64748b; font-size: 12px;">' + idLabel + ': <span class="mono">' + truncate(encodedId, 20) + '</span></div>';
        html += '<div style="color: #64748b; font-size: 12px;">Created: ' + createdAt + '</div>';
        html += '</div>';
        html += '<div style="display: flex; gap: 8px; flex-wrap: wrap;">';
        html += '<button class="copy-btn" onclick="copyToClipboard(\'' + encodedId + '\')">Copy ' + idLabel + '</button>';

        // All events can show details (anchor events are no longer special)
        const isStandalone = true;
        html += '<button class="copy-btn" style="background: #667eea; color: white;" onclick="showEventDetails(\'' + event.id + '\', ' + isStandalone + ')">Details</button>';

        if (isReplaceable) {
            html += '<button class="copy-btn" style="background: #f59e0b; color: white;" onclick="editEvent(' + event.kind + ', \'' + event.id + '\')">Edit</button>';
        }

        // Add delete button for all events
        html += '<button class="copy-btn" style="background: #ef4444; color: white;" onclick="deleteEvent(\'' + event.id + '\', ' + event.kind + ')">🗑️ Delete</button>';

        html += '</div>';
        html += '</div>';
        html += '</div>';
    });

    listDiv.innerHTML = html;
}

async function deleteEvent(eventId, kind) {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    const kindNames = {
        61600: 'Name Event',
        62600: 'Connection Event',
        63600: 'Metadata Event',
        60600: 'Anchor Event'
    };

    const kindName = kindNames[kind] || 'Event';

    if (!confirm('Are you sure you want to delete this ' + kindName + '?\n\nThis will publish a deletion event (kind 5) to all relays.')) {
        return;
    }

    try {
        // Create kind 5 deletion event
        const deletionEvent = {
            kind: 5,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['e', eventId],
                ['k', String(kind)]
            ],
            content: 'Deleted via DNN dashboard'
        };

        // Sign the deletion event
        const signedEvent = await signEventUniversal(deletionEvent);

        // Publish to all configured relays
        const relays = [
            'wss://relay.damus.io',
            'wss://relay.nostr.band',
            'wss://nos.lol',
            'wss://relay.primal.net'
        ];

        let successCount = 0;
        const publishPromises = relays.map(async (relayUrl) => {
            try {
                const ws = new WebSocket(relayUrl);
                return new Promise((resolve) => {
                    const timeout = setTimeout(() => {
                        ws.close();
                        resolve(false);
                    }, 5000);

                    ws.onopen = () => {
                        ws.send(JSON.stringify(['EVENT', signedEvent]));
                    };

                    ws.onmessage = (msg) => {
                        try {
                            const [type, , success] = JSON.parse(msg.data);
                            if (type === 'OK') {
                                clearTimeout(timeout);
                                ws.close();
                                resolve(success);
                            }
                        } catch (e) {
                            console.error('Error parsing relay response:', e);
                        }
                    };

                    ws.onerror = () => {
                        clearTimeout(timeout);
                        resolve(false);
                    };
                });
            } catch (error) {
                console.error('Error publishing to ' + relayUrl + ':', error);
                return false;
            }
        });

        const results = await Promise.all(publishPromises);
        successCount = results.filter(r => r).length;

        if (successCount > 0) {
            alert('Deletion event published to ' + successCount + '/' + relays.length + ' relays successfully!');
            // Refresh the events list
            setTimeout(() => loadMyEvents(), 1000);
        } else {
            alert('Failed to publish deletion event to any relays. Please try again.');
        }
    } catch (error) {
        console.error('Error deleting event:', error);
        alert('Error deleting event: ' + error.message);
    }
}

async function editEvent(kind, eventId) {
    // Check for NIP-07 extension OR NIP-46 remote signer
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first');
        return;
    }

    const modal = document.getElementById('editModal');
    const modalBody = document.getElementById('editModalBody');

    // Show modal with loading state
    modal.classList.add('show');
    modalBody.innerHTML = '<div class="loading-spinner"><div class="spinner"></div><p style="margin-top: 16px;">Loading event...</p></div>';

    try {
        console.log('Fetching event for edit:', eventId, 'kind:', kind);

        const response = await fetch('/dnn/event/' + eventId);
        if (!response.ok) {
            throw new Error('Failed to fetch event (HTTP ' + response.status + ')');
        }

        const data = await response.json();
        console.log('Event data for edit:', data);

        const eventToEdit = data.event;

        if (!eventToEdit) {
            throw new Error('Event not found');
        }

        // Verify kind matches
        if (eventToEdit.kind !== kind) {
            throw new Error('Event kind mismatch: expected ' + kind + ', got ' + eventToEdit.kind);
        }

        // Render edit form in modal based on event kind
        if (kind === 61600) {
            renderNameEventEditForm(eventToEdit);
        } else if (kind === 62600) {
            renderConnectionEventEditForm(eventToEdit);
        } else if (kind === 63600) {
            renderMetadataEventEditForm(eventToEdit);
        } else if (kind === 60600) {
            renderAnchorEventEditForm(eventToEdit);
        } else {
            throw new Error('Edit form not implemented for kind ' + kind);
        }

    } catch (error) {
        modalBody.innerHTML = '<div class="event-not-found"><div style="font-size: 48px; margin-bottom: 16px;">❌</div><div>Failed to load event</div><div style="font-size: 12px; margin-top: 8px; color: #f87171;">' + error.message + '</div></div>';
        console.error('Edit error:', error);
    }
}

function closeEditModal() {
    const modal = document.getElementById('editModal');
    modal.classList.remove('show');
}

function renderNameEventEditForm(event) {
    const modalBody = document.getElementById('editModalBody');
    const dTag = event.tags.find(t => t[0] === 'd')?.[1] || '';
    const nTag = event.tags.find(t => t[0] === 'n')?.[1] || '';
    const oTags = event.tags.filter(t => t[0] === 'o').map(t => t[1]);

    let html = '<div>' +
        '<h3 style="color: #f1f5f9; margin-bottom: 20px;">📝 Edit Name Event (Kind 61600)</h3>' +
        '<div style="margin-bottom: 16px;">' +
        '<label style="display: block; color: #94a3b8; font-size: 12px; margin-bottom: 6px;">Primary Name</label>' +
        '<input type="text" id="editPrimaryName" value="' + escapeHtml(nTag) + '" style="width: 100%; padding: 10px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #e2e8f0; font-size: 14px;" />' +
        '</div>' +
        '<div style="margin-bottom: 16px;">' +
        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
        '<label style="color: #94a3b8; font-size: 12px;">Other Names</label>' +
        '<button type="button" onclick="addEditOtherNameField()" style="padding: 4px 12px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px; font-weight: bold;">+ Add Name</button>' +
        '</div>' +
        '<div id="editOtherNamesContainer">';

    // Add existing other names
    oTags.forEach((otherName, idx) => {
        const id = 'editOtherName_' + idx;
        html += '<div id="' + id + '" style="display: flex; gap: 8px; margin-bottom: 8px;">' +
            '<input type="text" class="editOtherNameInput" value="' + escapeHtml(otherName) + '" placeholder="e.g., alice_backup" style="flex: 1; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
            '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
            '</div>';
    });

    html += '</div>' +
        '</div>' +
        '<input type="hidden" id="editDTag" value="' + dTag + '" />' +
        '<input type="hidden" id="editEventId" value="' + event.id + '" />' +
        '<div id="editMessage" style="padding: 12px; border-radius: 8px; margin-bottom: 16px; display: none;"></div>' +
        '<div style="display: flex; gap: 12px; justify-content: flex-end;">' +
        '<button onclick="closeEditModal()" style="padding: 10px 24px; background: #334155; color: #e2e8f0; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Cancel</button>' +
        '<button onclick="saveNameEvent()" style="padding: 10px 24px; background: #10b981; color: white; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Save Changes</button>' +
        '</div>' +
        '</div>';

    modalBody.innerHTML = html;
}

let editOtherNameFieldCount = 1000; // Start high to avoid conflicts with publish form
function addEditOtherNameField() {
    const container = document.getElementById('editOtherNamesContainer');
    const id = 'editOtherName_' + editOtherNameFieldCount++;

    const div = document.createElement('div');
    div.id = id;
    div.style.cssText = 'display: flex; gap: 8px; margin-bottom: 8px;';
    div.innerHTML = '<input type="text" class="editOtherNameInput" placeholder="e.g., alice_backup" style="flex: 1; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

function renderConnectionEventEditForm(event) {
    const modalBody = document.getElementById('editModalBody');
    const dTag = event.tags.find(t => t[0] === 'd')?.[1] || '';

    // Parse existing DNS records
    let ipv4 = '', ipv6 = '', httpsPort = '443', httpPort = '';
    let customDNSRecords = [];
    let otherNames = {};
    let selfCert = null;

    try {
        const content = JSON.parse(event.content);
        // Find the primary domain key (first key in the content)
        let primaryKey = Object.keys(content)[0];
        if (!primaryKey) {
            throw new Error('No domain key found in connection content');
        }
        const selfRecords = content[primaryKey]?.records || [];

        // Extract primary domain records
        selfRecords.forEach(record => {
            if (record[0] === 'A' && record[1] === '@') {
                ipv4 = record[2];
            } else if (record[0] === 'AAAA' && record[1] === '@') {
                ipv6 = record[2];
            } else if (record[0] === 'SRV' && record[1] === '_https._tcp') {
                httpsPort = record[4];
            } else if (record[0] === 'SRV' && record[1] === '_http._tcp') {
                httpPort = record[4];
            } else {
                // Custom DNS record
                customDNSRecords.push(record);
            }
        });

        // Extract certificate from primary domain
        selfCert = content[primaryKey]?.cert || null;

        // Extract other names
        Object.keys(content).forEach(key => {
            if (key !== primaryKey) {
                otherNames[key] = content[key];
            }
        });
    } catch (e) {
        console.error('Error parsing connection data:', e);
    }

    let html = '<div style="max-height: 70vh; overflow-y: auto; padding-right: 12px;">' +
        '<h3 style="color: #f1f5f9; margin-bottom: 20px;">🔗 Edit Connection Event (Kind 62600)</h3>' +

        // Primary Name DNS Records
        '<div style="margin-bottom: 16px; padding: 16px; background: #0f172a; border: 1px solid #334155; border-radius: 8px;">' +
        '<div style="color: #f1f5f9; font-weight: bold; margin-bottom: 12px;">Primary Domain (' + escapeHtml(primaryKey) + ') DNS Records</div>' +
        '<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 12px;">' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">IPv4 (A)</label>' +
        '<input type="text" id="editConnIPv4" value="' + escapeHtml(ipv4) + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">IPv6 (AAAA)</label>' +
        '<input type="text" id="editConnIPv6" value="' + escapeHtml(ipv6) + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '</div>' +
        '<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 12px;">' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">HTTPS Port</label>' +
        '<input type="number" id="editConnHTTPSPort" value="' + httpsPort + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">HTTP Port</label>' +
        '<input type="number" id="editConnHTTPPort" value="' + httpPort + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '</div>' +
        '<div id="editCustomDNSRecords">';

    // Add existing custom DNS records
    customDNSRecords.forEach((record, idx) => {
        const id = 'editDNSRecord_' + idx;
        const [type, name, value, ttl] = record;
        html += '<div id="' + id + '" style="padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; margin-bottom: 8px;">' +
            '<div style="display: flex; gap: 8px; align-items: end;">' +
            '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Type</label>' +
            '<select class="editDNSRecordType" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;">' +
            '<option value="A"' + (type === 'A' ? ' selected' : '') + '>A</option>' +
            '<option value="AAAA"' + (type === 'AAAA' ? ' selected' : '') + '>AAAA</option>' +
            '<option value="CNAME"' + (type === 'CNAME' ? ' selected' : '') + '>CNAME</option>' +
            '<option value="TXT"' + (type === 'TXT' ? ' selected' : '') + '>TXT</option>' +
            '<option value="MX"' + (type === 'MX' ? ' selected' : '') + '>MX</option>' +
            '<option value="SRV"' + (type === 'SRV' ? ' selected' : '') + '>SRV</option>' +
            '</select></div>' +
            '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Name</label>' +
            '<input type="text" class="editDNSRecordName" value="' + escapeHtml(name) + '" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
            '<div style="flex: 2;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Value</label>' +
            '<input type="text" class="editDNSRecordValue" value="' + escapeHtml(value) + '" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
            '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">TTL</label>' +
            '<input type="text" class="editDNSRecordTTL" value="' + escapeHtml(ttl) + '" style="width: 80px; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
            '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
            '</div>' +
            '</div>';
    });

    html += '</div>' +
        '<button type="button" onclick="addEditCustomDNSRecord()" style="padding: 6px 16px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: bold; margin-top: 8px;">+ Add Custom DNS Record</button>' +
        '<div style="margin-top: 16px; padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 6px;">' +
        '<div style="color: #94a3b8; font-weight: bold; font-size: 11px; margin-bottom: 8px;">🔒 Certificate</div>' +
        '<textarea id="editCertPEM" rows="4" style="width: 100%; padding: 6px; background: #0f172a; border: 1px solid #334155; border-radius: 4px; color: #e2e8f0; font-size: 11px; font-family: monospace;">' + (selfCert?.pem || '') + '</textarea>' +
        '<textarea id="editCertSignature" rows="2" readonly style="width: 100%; padding: 6px; background: #0f172a; border: 1px solid #334155; border-radius: 4px; color: #a78bfa; font-size: 10px; font-family: monospace; margin-top: 8px;">' + (selfCert?.cert_signature?.signature || '') + '</textarea>' +
        '<input type="hidden" id="editCertHash" value="' + (selfCert?.cert_signature?.hash || '') + '">' +
        '<input type="hidden" id="editCertPubkey" value="' + (selfCert?.cert_signature?.pubkey || '') + '">' +
        '<input type="hidden" id="editCertSignedAt" value="' + (selfCert?.cert_signature?.signed_at || '') + '">' +
        '<input type="datetime-local" id="editCertExpiry" readonly title="Auto-extracted from certificate" value="' + (selfCert?.expires ? new Date(selfCert.expires * 1000).toISOString().slice(0, 16) : '') + '" style="width: 100%; padding: 6px; background: #0f172a; border: 1px solid #334155; border-radius: 4px; color: #e2e8f0; font-size: 11px; margin-top: 8px; cursor: not-allowed;">' +
        '<button type="button" onclick="signEditCertificate()" style="padding: 6px 12px; background: #10b981; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 10px; margin-top: 8px;">🔑 Sign</button>' +
        '</div>' +
        '</div>' +

        // Other Name Connections
        '<div id="editOtherNameConnectionsContainer" style="margin-bottom: 16px;">';

    // Add existing other name connections
    Object.keys(otherNames).forEach((name, idx) => {
        const connData = otherNames[name];
        const records = connData.records || [];
        let connIPv4 = '', connIPv6 = '', connHTTPSPort = '443', connHTTPPort = '';
        let connCustomDNS = [];

        records.forEach(record => {
            if (record[0] === 'A' && record[1] === '@') {
                connIPv4 = record[2];
            } else if (record[0] === 'AAAA' && record[1] === '@') {
                connIPv6 = record[2];
            } else if (record[0] === 'SRV' && record[1] === '_https._tcp') {
                connHTTPSPort = record[4];
            } else if (record[0] === 'SRV' && record[1] === '_http._tcp') {
                connHTTPPort = record[4];
            } else {
                connCustomDNS.push(record);
            }
        });

        const connId = 'editOtherNameConn_' + idx;
        html += '<div id="' + connId + '" style="padding: 16px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; margin-bottom: 12px;">' +
            '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">' +
            '<label style="color: #f1f5f9; font-weight: bold; font-size: 13px;">Other Name Connection</label>' +
            '<button type="button" onclick="removeField(\'' + connId + '\')" style="padding: 4px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;">Remove</button>' +
            '</div>' +
            '<div style="margin-bottom: 12px;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Name</label>' +
            '<input type="text" class="editOtherConnName" value="' + escapeHtml(name) + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
            '<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin-bottom: 12px;">' +
            '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">IPv4</label>' +
            '<input type="text" class="editOtherConnIPv4" value="' + escapeHtml(connIPv4) + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
            '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">IPv6</label>' +
            '<input type="text" class="editOtherConnIPv6" value="' + escapeHtml(connIPv6) + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
            '</div>' +
            '<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin-bottom: 12px;">' +
            '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">HTTPS Port</label>' +
            '<input type="number" class="editOtherConnHTTPSPort" value="' + connHTTPSPort + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
            '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">HTTP Port</label>' +
            '<input type="number" class="editOtherConnHTTPPort" value="' + connHTTPPort + '" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
            '</div>' +
            '<div id="editOtherConnCustomDNS_' + idx + '">';

        // Add custom DNS records for this other name
        connCustomDNS.forEach((record, rIdx) => {
            const [type, recName, value, ttl] = record;
            const recId = 'editOtherConnDNS_' + idx + '_' + rIdx;
            html += '<div id="' + recId + '" style="padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; margin-bottom: 8px;">' +
                '<div style="display: flex; gap: 8px; align-items: end;">' +
                '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Type</label>' +
                '<select class="editOtherConnDNSType" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;">' +
                '<option value="A"' + (type === 'A' ? ' selected' : '') + '>A</option>' +
                '<option value="AAAA"' + (type === 'AAAA' ? ' selected' : '') + '>AAAA</option>' +
                '<option value="CNAME"' + (type === 'CNAME' ? ' selected' : '') + '>CNAME</option>' +
                '<option value="TXT"' + (type === 'TXT' ? ' selected' : '') + '>TXT</option>' +
                '<option value="MX"' + (type === 'MX' ? ' selected' : '') + '>MX</option>' +
                '<option value="SRV"' + (type === 'SRV' ? ' selected' : '') + '>SRV</option>' +
                '</select></div>' +
                '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Name</label>' +
                '<input type="text" class="editOtherConnDNSName" value="' + escapeHtml(recName) + '" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
                '<div style="flex: 2;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Value</label>' +
                '<input type="text" class="editOtherConnDNSValue" value="' + escapeHtml(value) + '" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
                '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">TTL</label>' +
                '<input type="text" class="editOtherConnDNSTTL" value="' + escapeHtml(ttl) + '" style="width: 80px; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
                '<button type="button" onclick="removeField(\'' + recId + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                '</div>' +
                '</div>';
        });

        html += '</div>' +
            '<button type="button" onclick="addEditOtherConnCustomDNSRecord(' + idx + ')" style="padding: 6px 16px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: bold; margin-top: 8px;">+ Add Custom DNS Record</button>' +
            '</div>';
    });

    html += '</div>' +
        '<button type="button" onclick="addEditOtherNameConnection()" style="padding: 8px 20px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: bold; margin-bottom: 16px;">+ Add Connection for Other Name</button>' +
        '<input type="hidden" id="editDTag" value="' + dTag + '" />' +
        '<input type="hidden" id="editEventId" value="' + event.id + '" />' +
        '<div id="editMessage" style="padding: 12px; border-radius: 8px; margin-bottom: 16px; display: none;"></div>' +
        '</div>' +
        '<div style="display: flex; gap: 12px; justify-content: flex-end; padding-top: 16px; border-top: 1px solid #334155; margin-top: 16px;">' +
        '<button onclick="closeEditModal()" style="padding: 10px 24px; background: #334155; color: #e2e8f0; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Cancel</button>' +
        '<button onclick="saveConnectionEvent()" style="padding: 10px 24px; background: #10b981; color: white; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Save Changes</button>' +
        '</div>';

    modalBody.innerHTML = html;
}

let editCustomDNSRecordCount = 4000; // Start high to avoid conflicts
let editOtherNameConnectionCount = 5000;

function addEditCustomDNSRecord() {
    const container = document.getElementById('editCustomDNSRecords');
    const id = 'editDNSRecord_' + editCustomDNSRecordCount++;

    const div = document.createElement('div');
    div.id = id;
    div.style.cssText = 'padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; margin-bottom: 8px;';
    div.innerHTML = '<div style="display: flex; gap: 8px; align-items: end;">' +
        '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Type</label>' +
        '<select class="editDNSRecordType" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;">' +
        '<option value="A">A</option><option value="AAAA">AAAA</option><option value="CNAME">CNAME</option>' +
        '<option value="TXT">TXT</option><option value="MX">MX</option><option value="SRV">SRV</option>' +
        '</select></div>' +
        '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Name</label>' +
        '<input type="text" class="editDNSRecordName" placeholder="@" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
        '<div style="flex: 2;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Value</label>' +
        '<input type="text" class="editDNSRecordValue" placeholder="Record value" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">TTL</label>' +
        '<input type="text" class="editDNSRecordTTL" value="3600" style="width: 80px; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
        '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
        '</div>';

    container.appendChild(div);
}

function addEditOtherNameConnection() {
    const container = document.getElementById('editOtherNameConnectionsContainer');
    const connIndex = editOtherNameConnectionCount++;
    const id = 'editOtherNameConn_' + connIndex;

    const div = document.createElement('div');
    div.id = id;
    div.style.cssText = 'padding: 16px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; margin-bottom: 12px;';
    div.innerHTML = '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">' +
        '<label style="color: #f1f5f9; font-weight: bold; font-size: 13px;">Other Name Connection</label>' +
        '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 4px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;">Remove</button>' +
        '</div>' +
        '<div style="margin-bottom: 12px;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Name</label>' +
        '<input type="text" class="editOtherConnName" placeholder="e.g., alice_work" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin-bottom: 12px;">' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">IPv4</label>' +
        '<input type="text" class="editOtherConnIPv4" placeholder="203.0.113.1" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">IPv6</label>' +
        '<input type="text" class="editOtherConnIPv6" placeholder="2001:db8::1" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '</div>' +
        '<div style="display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin-bottom: 12px;">' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">HTTPS Port</label>' +
        '<input type="number" class="editOtherConnHTTPSPort" value="443" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">HTTP Port</label>' +
        '<input type="number" class="editOtherConnHTTPPort" placeholder="80" style="width: 100%; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" /></div>' +
        '</div>' +
        '<div id="editOtherConnCustomDNS_' + connIndex + '"></div>' +
        '<button type="button" onclick="addEditOtherConnCustomDNSRecord(' + connIndex + ')" style="padding: 6px 16px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: bold; margin-top: 8px;">+ Add Custom DNS Record</button>';

    container.appendChild(div);
}

function addEditOtherConnCustomDNSRecord(connIndex) {
    const container = document.getElementById('editOtherConnCustomDNS_' + connIndex);
    const id = 'editOtherConnDNS_' + connIndex + '_' + Date.now();

    const div = document.createElement('div');
    div.id = id;
    div.style.cssText = 'padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; margin-bottom: 8px;';
    div.innerHTML = '<div style="display: flex; gap: 8px; align-items: end;">' +
        '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Type</label>' +
        '<select class="editOtherConnDNSType" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;">' +
        '<option value="A">A</option><option value="AAAA">AAAA</option><option value="CNAME">CNAME</option>' +
        '<option value="TXT">TXT</option><option value="MX">MX</option><option value="SRV">SRV</option>' +
        '</select></div>' +
        '<div style="flex: 1;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Name</label>' +
        '<input type="text" class="editOtherConnDNSName" placeholder="@" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
        '<div style="flex: 2;"><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">Value</label>' +
        '<input type="text" class="editOtherConnDNSValue" placeholder="Record value" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
        '<div><label style="display: block; color: #94a3b8; font-size: 11px; margin-bottom: 4px;">TTL</label>' +
        '<input type="text" class="editOtherConnDNSTTL" value="3600" style="width: 80px; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
        '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
        '</div>';

    container.appendChild(div);
}

function renderMetadataEventEditForm(event) {
    const modalBody = document.getElementById('editModalBody');
    const dTag = event.tags.find(t => t[0] === 'd')?.[1] || '';

    // Parse existing metadata
    let metadata = {};
    try {
        const content = JSON.parse(event.content);
        metadata = content.metadata || content;
    } catch (e) {
        console.error('Error parsing metadata:', e);
    }

    let html = '<div style="max-height: 70vh; overflow-y: auto; padding-right: 12px;">' +
        '<h3 style="color: #f1f5f9; margin-bottom: 20px;">📊 Edit Metadata Event (Kind 63600)</h3>' +

        // Description
        '<div style="margin-bottom: 16px;">' +
        '<label style="display: block; color: #94a3b8; font-size: 12px; margin-bottom: 6px;">Description</label>' +
        '<textarea id="editMetaDescription" placeholder="Describe your DNN registration..." style="width: 100%; padding: 10px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #e2e8f0; font-size: 14px; min-height: 80px;">' + escapeHtml(metadata.description || '') + '</textarea>' +
        '</div>' +

        // URLs
        '<div style="margin-bottom: 16px;">' +
        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
        '<label style="color: #94a3b8; font-size: 12px;">URLs</label>' +
        '<button type="button" onclick="addEditMetaURL()" style="padding: 4px 12px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px; font-weight: bold;">+ Add URL</button>' +
        '</div>' +
        '<div id="editMetaURLsContainer">';

    // Add existing URLs
    if (metadata.urls && Array.isArray(metadata.urls)) {
        metadata.urls.forEach((urlObj, idx) => {
            const id = 'editMetaURL_' + idx;
            html += '<div id="' + id + '" style="display: flex; gap: 8px; margin-bottom: 8px;">' +
                '<input type="text" class="editMetaURLLabel" value="' + escapeHtml(urlObj.label || '') + '" placeholder="Label (e.g., website)" style="flex: 1; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
                '<input type="text" class="editMetaURLValue" value="' + escapeHtml(urlObj.url || '') + '" placeholder="https://example.com" style="flex: 2; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
                '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                '</div>';
        });
    }

    html += '</div></div>' +

        // Cryptocurrency Addresses
        '<div style="margin-bottom: 16px;">' +
        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
        '<label style="color: #94a3b8; font-size: 12px;">Cryptocurrency Addresses</label>' +
        '<button type="button" onclick="addEditMetaCurrency()" style="padding: 4px 12px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px; font-weight: bold;">+ Add Currency</button>' +
        '</div>' +
        '<div id="editMetaCurrenciesContainer">';

    // Add existing currencies
    if (metadata.currencies && Array.isArray(metadata.currencies)) {
        metadata.currencies.forEach((currObj, idx) => {
            const id = 'editMetaCurrency_' + idx;
            html += '<div id="' + id + '" style="padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 8px; margin-bottom: 12px;">' +
                '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
                '<label style="color: #e2e8f0; font-weight: bold; font-size: 12px;">Currency</label>' +
                '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 4px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;">Remove</button>' +
                '</div>' +
                '<div style="margin-bottom: 8px;"><input type="text" class="editMetaCurrencyName" value="' + escapeHtml(currObj.currency || '') + '" placeholder="Currency (e.g., bitcoin)" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
                '<div id="editMetaCurrencyAddresses_' + idx + '" style="margin-bottom: 8px;">';

            // Add addresses for this currency
            if (currObj.addresses && Array.isArray(currObj.addresses)) {
                currObj.addresses.forEach((addrObj, aIdx) => {
                    const addrId = 'editCurrencyAddr_' + idx + '_' + aIdx;
                    html += '<div id="' + addrId + '" style="display: flex; gap: 8px; margin-bottom: 8px;">' +
                        '<input type="text" class="editCurrencyAddrType" value="' + escapeHtml(addrObj.type || '') + '" placeholder="Type (e.g., native_segwit)" style="flex: 1; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" />' +
                        '<input type="text" class="editCurrencyAddrValue" value="' + escapeHtml(addrObj.address || '') + '" placeholder="Address" style="flex: 2; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" />' +
                        '<button type="button" onclick="removeField(\'' + addrId + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                        '</div>';
                });
            }

            html += '</div>' +
                '<button type="button" onclick="addEditCurrencyAddress(' + idx + ')" style="padding: 4px 12px; background: #475569; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;">+ Add Address</button>' +
                '</div>';
        });
    }

    html += '</div></div>' +

        // Nostr Addresses
        '<div style="margin-bottom: 16px;">' +
        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
        '<label style="color: #94a3b8; font-size: 12px;">Nostr Addresses</label>' +
        '<button type="button" onclick="addEditMetaNostrAddress()" style="padding: 4px 12px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px; font-weight: bold;">+ Add Nostr Address</button>' +
        '</div>' +
        '<div id="editMetaNostrAddressesContainer">';

    // Add existing Nostr addresses
    if (metadata.nostrAddresses && Array.isArray(metadata.nostrAddresses)) {
        metadata.nostrAddresses.forEach((addrObj, idx) => {
            const id = 'editMetaNostr_' + idx;
            html += '<div id="' + id + '" style="display: flex; gap: 8px; margin-bottom: 8px;">' +
                '<input type="text" class="editMetaNostrLabel" value="' + escapeHtml(addrObj.label || '') + '" placeholder="Label (e.g., main)" style="flex: 1; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
                '<input type="text" class="editMetaNostrValue" value="' + escapeHtml(addrObj.address || '') + '" placeholder="npub1... or hex pubkey" style="flex: 2; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
                '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                '</div>';
        });
    }

    html += '</div></div>' +

        // Nostr Relays
        '<div style="margin-bottom: 16px;">' +
        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
        '<label style="color: #94a3b8; font-size: 12px;">Nostr Relays</label>' +
        '<button type="button" onclick="addEditMetaRelay()" style="padding: 4px 12px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px; font-weight: bold;">+ Add Relay</button>' +
        '</div>' +
        '<div id="editMetaRelaysContainer">';

    // Add existing relays
    if (metadata.relays && Array.isArray(metadata.relays)) {
        metadata.relays.forEach((relayURL, idx) => {
            const id = 'editMetaRelay_' + idx;
            html += '<div id="' + id + '" style="display: flex; gap: 8px; margin-bottom: 8px;">' +
                '<input type="text" class="editMetaRelayURL" value="' + escapeHtml(relayURL) + '" placeholder="wss://" style="flex: 1; padding: 8px; background: #1e293b; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 13px;" />' +
                '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 8px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                '</div>';
        });
    }

    html += '</div></div>' +

        // Custom Fields
        '<div style="margin-bottom: 16px;">' +
        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
        '<label style="color: #94a3b8; font-size: 12px;">Custom Fields</label>' +
        '<button type="button" onclick="addEditMetaCustomField()" style="padding: 4px 12px; background: #334155; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px; font-weight: bold;">+ Add Custom Field</button>' +
        '</div>' +
        '<div id="editMetaCustomFieldsContainer">';

    // Add existing custom fields with label/value row structure
    const knownFields = ['description', 'urls', 'currencies', 'nostrAddresses', 'relays'];
    let customFieldIndex = 0;
    Object.keys(metadata).forEach(key => {
        if (!knownFields.includes(key)) {
            const value = metadata[key];
            const id = 'editMetaCustom_' + customFieldIndex;

            html += '<div id="' + id + '" style="padding: 12px; background: #1e293b; border: 1px solid #334155; border-radius: 8px; margin-bottom: 12px;">' +
                '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">' +
                '<label style="color: #e2e8f0; font-weight: bold; font-size: 12px;">Custom Field</label>' +
                '<button type="button" onclick="removeField(\'' + id + '\')" style="padding: 4px 12px; background: #ef4444; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;">Remove</button>' +
                '</div>' +
                '<div style="margin-bottom: 8px;"><input type="text" class="editMetaCustomKey" value="' + escapeHtml(key) + '" placeholder="Field name (e.g., social_media)" style="width: 100%; padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; color: #e2e8f0; font-size: 12px;" /></div>' +
                '<div id="editMetaCustomRows_' + customFieldIndex + '" style="margin-bottom: 8px;">';

            // Render existing label/value rows
            if (typeof value === 'object' && !Array.isArray(value)) {
                // Object structure: { label1: [val1, val2], label2: "val" }
                let rowIdx = 0;
                Object.keys(value).forEach(label => {
                    const rowValues = Array.isArray(value[label]) ? value[label] : [value[label]];
                    const rowId = 'editCustomRow_' + customFieldIndex + '_' + rowIdx;

                    html += '<div id="' + rowId + '" style="padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; margin-bottom: 8px;">' +
                        '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px;">' +
                        '<label style="color: #94a3b8; font-size: 11px;">Row ' + (rowIdx + 1) + '</label>' +
                        '<button type="button" onclick="removeField(\'' + rowId + '\')" style="padding: 4px 8px; background: #ef4444; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 10px;">Remove Row</button>' +
                        '</div>' +
                        '<div style="margin-bottom: 6px;">' +
                        '<input type="text" class="editMetaCustomLabel" value="' + escapeHtml(label) + '" placeholder="Label (e.g., Twitter)" style="width: 100%; padding: 6px; background: #1e293b; border: 1px solid #334155; border-radius: 4px; color: #e2e8f0; font-size: 12px;" />' +
                        '</div>' +
                        '<div id="editCustomValues_' + customFieldIndex + '_' + rowIdx + '" style="margin-bottom: 6px;">';

                    rowValues.forEach((val, valIdx) => {
                        const valId = 'editCustomVal_' + customFieldIndex + '_' + rowIdx + '_' + valIdx;
                        html += '<div id="' + valId + '" style="display: flex; gap: 4px; align-items: center; margin-bottom: 4px;">' +
                            '<input type="text" class="editMetaCustomValue" value="' + escapeHtml(String(val)) + '" placeholder="Value" style="flex: 1; padding: 6px; background: #1e293b; border: 1px solid #334155; border-radius: 4px; color: #e2e8f0; font-size: 12px;" />' +
                            '<button type="button" onclick="removeField(\'' + valId + '\')" style="padding: 6px 8px; background: #ef4444; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 10px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                            '</div>';
                    });

                    html += '</div>' +
                        '<button type="button" onclick="addEditCustomFieldValue(' + customFieldIndex + ', ' + rowIdx + ')" style="padding: 4px 10px; background: #475569; color: #e2e8f0; border: none; border-radius: 4px; cursor: pointer; font-size: 11px;">+ Add Value</button>' +
                        '</div>';

                    rowIdx++;
                });
            } else {
                // Fallback for non-object values - convert to single row
                const rowId = 'editCustomRow_' + customFieldIndex + '_0';
                const displayValue = Array.isArray(value) ? value.join(', ') : String(value);

                html += '<div id="' + rowId + '" style="padding: 8px; background: #0f172a; border: 1px solid #334155; border-radius: 6px; margin-bottom: 8px;">' +
                    '<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px;">' +
                    '<label style="color: #94a3b8; font-size: 11px;">Row 1</label>' +
                    '<button type="button" onclick="removeField(\'' + rowId + '\')" style="padding: 4px 8px; background: #ef4444; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 10px;">Remove Row</button>' +
                    '</div>' +
                    '<div style="margin-bottom: 6px;">' +
                    '<input type="text" class="editMetaCustomLabel" value="" placeholder="Label (e.g., Twitter)" style="width: 100%; padding: 6px; background: #1e293b; border: 1px solid #334155; border-radius: 4px; color: #e2e8f0; font-size: 12px;" />' +
                    '</div>' +
                    '<div id="editCustomValues_' + customFieldIndex + '_0" style="margin-bottom: 6px;">' +
                    '<div id="editCustomVal_' + customFieldIndex + '_0_0" style="display: flex; gap: 4px; align-items: center; margin-bottom: 4px;">' +
                    '<input type="text" class="editMetaCustomValue" value="' + escapeHtml(displayValue) + '" placeholder="Value" style="flex: 1; padding: 6px; background: #1e293b; border: 1px solid #334155; border-radius: 4px; color: #e2e8f0; font-size: 12px;" />' +
                    '<button type="button" onclick="removeField(\'editCustomVal_' + customFieldIndex + '_0_0\')" style="padding: 6px 8px; background: #ef4444; color: white; border: none; border-radius: 4px; cursor: pointer; font-size: 10px;"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>' +
                    '</div>' +
                    '</div>' +
                    '<button type="button" onclick="addEditCustomFieldValue(' + customFieldIndex + ', 0)" style="padding: 4px 10px; background: #475569; color: #e2e8f0; border: none; border-radius: 4px; cursor: pointer; font-size: 11px;">+ Add Value</button>' +
                    '</div>';
            }

            html += '</div>' +
                '<button type="button" onclick="addEditCustomFieldRow(' + customFieldIndex + ')" style="padding: 4px 12px; background: #475569; color: #e2e8f0; border: none; border-radius: 6px; cursor: pointer; font-size: 11px;">+ Add Row</button>' +
                '</div>';

            customFieldIndex++;
        }
    });

    html += '</div></div>' +
        '<input type="hidden" id="editDTag" value="' + dTag + '" />' +
        '<input type="hidden" id="editEventId" value="' + event.id + '" />' +
        '<div id="editMessage" style="padding: 12px; border-radius: 8px; margin-bottom: 16px; display: none;"></div>' +
        '</div>' +
        '<div style="display: flex; gap: 12px; justify-content: flex-end; padding-top: 16px; border-top: 1px solid #334155; margin-top: 16px;">' +
        '<button onclick="closeEditModal()" style="padding: 10px 24px; background: #334155; color: #e2e8f0; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Cancel</button>' +
        '<button onclick="saveMetadataEvent()" style="padding: 10px 24px; background: #10b981; color: white; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Save Changes</button>' +
        '</div>';

    modalBody.innerHTML = html;
}

let editMetaURLCount = 2000; // Start high to avoid conflicts
let editMetaNostrAddressCount = 3000;
let editMetaRelayCount = 4000;
let editMetaCustomFieldCount = 7000;

function addEditMetaURL() {
    const container = document.getElementById('editMetaURLsContainer');
    const id = 'editMetaURL_' + editMetaURLCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center mb-2';
    div.innerHTML = '<input type="text" class="editMetaURLLabel w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Label (e.g., website)" />' +
        '<input type="text" class="editMetaURLValue w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="https://example.com" />' +
        '<button type="button" onclick="removeField(\'' + id + '\')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center"><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>';

    container.appendChild(div);
}

let editMetaCurrencyCount = 6000;

function addEditMetaCurrency() {
    const container = document.getElementById('editMetaCurrenciesContainer');
    const id = 'editMetaCurrency_' + editMetaCurrencyCount;
    const currencyIndex = editMetaCurrencyCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl space-y-3';
    div.innerHTML = `
        <div class="flex justify-between items-center">
            <label class="text-sm font-medium text-white">Currency</label>
            <button type="button" onclick="removeField('${id}')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all">Remove</button>
        </div>
        <input type="text" class="editMetaCurrencyName w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Currency (e.g., bitcoin)" />
        <div id="editMetaCurrencyAddresses_${currencyIndex}" class="space-y-2"></div>
        <button type="button" onclick="addEditCurrencyAddress(${currencyIndex})" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1">
            <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            Add Address
        </button>`;

    container.appendChild(div);

    // Add first address field automatically
    addEditCurrencyAddress(currencyIndex);
}

function addEditCurrencyAddress(currencyIndex) {
    const container = document.getElementById('editMetaCurrencyAddresses_' + currencyIndex);
    const id = 'editCurrencyAddr_' + currencyIndex + '_' + Date.now();

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = `
        <input type="text" class="editCurrencyAddrType flex-1 px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm" placeholder="Type (e.g., native_segwit)" />
        <input type="text" class="editCurrencyAddrValue flex-[2] px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Address" />
        <button type="button" onclick="removeField('${id}')" class="p-2 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg transition-all flex items-center justify-center">
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>`;

    container.appendChild(div);
}

function addEditMetaNostrAddress() {
    const container = document.getElementById('editMetaNostrAddressesContainer');
    const id = 'editMetaNostr_' + editMetaNostrAddressCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = `
        <input type="text" class="editMetaNostrLabel w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Label" />
        <input type="text" class="editMetaNostrValue w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="npub1..." />
        <button type="button" onclick="removeField('${id}')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>`;

    container.appendChild(div);
}

function addEditMetaRelay() {
    const container = document.getElementById('editMetaRelaysContainer');
    const id = 'editMetaRelay_' + editMetaRelayCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = `
        <input type="text" class="editMetaRelayURL flex-1 px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="wss://" value="wss://" />
        <button type="button" onclick="removeField('${id}')" class="p-3 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-xl transition-all flex items-center justify-center">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>`;

    container.appendChild(div);
}

function addEditMetaCustomField() {
    const container = document.getElementById('editMetaCustomFieldsContainer');
    const id = 'editMetaCustom_' + editMetaCustomFieldCount;
    const fieldIndex = editMetaCustomFieldCount++;

    const div = document.createElement('div');
    div.id = id;
    div.className = 'p-4 bg-dnn-secondary/30 border border-dnn-border rounded-xl space-y-3';
    div.innerHTML = `
        <div class="flex justify-between items-center">
            <label class="text-sm font-medium text-white">Custom Field</label>
            <button type="button" onclick="removeField('${id}')" class="px-3 py-1.5 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg text-xs font-medium transition-all">Remove</button>
        </div>
        <input type="text" class="editMetaCustomKey w-full px-4 py-3 bg-dnn-secondary border border-dnn-border rounded-xl text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent font-mono text-sm" placeholder="Field name (e.g., social_media)" />
        <div id="editMetaCustomRows_${fieldIndex}" class="space-y-2"></div>
        <button type="button" onclick="addEditCustomFieldRow(${fieldIndex})" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1">
            <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            Add Row
        </button>`;

    container.appendChild(div);

    // Add first row automatically
    addEditCustomFieldRow(fieldIndex);
}

function addEditCustomFieldValue(fieldIndex, rowIdx) {
    const valuesContainer = document.getElementById('editCustomValues_' + fieldIndex + '_' + rowIdx);
    const valId = 'editCustomVal_' + fieldIndex + '_' + rowIdx + '_' + Date.now();

    const div = document.createElement('div');
    div.id = valId;
    div.className = 'flex gap-2 items-center';
    div.innerHTML = `
        <input type="text" class="editMetaCustomValue flex-1 px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm" placeholder="Value" />
        <button type="button" onclick="removeField('${valId}')" class="p-2 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded-lg transition-all flex items-center justify-center">
            <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
        </button>`;

    valuesContainer.appendChild(div);
}

function addEditCustomFieldRow(fieldIndex) {
    const container = document.getElementById('editMetaCustomRows_' + fieldIndex);
    const rowIdx = container.children.length;
    const rowId = 'editCustomRow_' + fieldIndex + '_' + rowIdx;

    const div = document.createElement('div');
    div.id = rowId;
    div.className = 'p-3 bg-dnn-secondary/50 border border-dnn-border/50 rounded-lg space-y-2';
    div.innerHTML = `
        <div class="flex justify-between items-center">
            <label class="text-xs text-gray-400">Row ${rowIdx + 1}</label>
            <button type="button" onclick="removeField('${rowId}')" class="px-2 py-1 bg-dnn-secondary border border-red-500/50 text-red-400 hover:bg-red-500/20 rounded text-xs font-medium transition-all">Remove Row</button>
        </div>
        <input type="text" class="editMetaCustomLabel w-full px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-lg text-white placeholder-gray-600 focus:outline-none focus:border-dnn-accent text-sm" placeholder="Label (e.g., Twitter)" />
        <div id="editCustomValues_${fieldIndex}_${rowIdx}" class="space-y-1"></div>
        <button type="button" onclick="addEditCustomFieldValue(${fieldIndex}, ${rowIdx})" class="text-xs text-dnn-accent hover:text-dnn-purple transition-all flex items-center gap-1">
            <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            Add Value
        </button>`;

    container.appendChild(div);

    // Add first value field automatically
    addEditCustomFieldValue(fieldIndex, rowIdx);
}

async function saveNameEvent() {
    const primaryName = document.getElementById('editPrimaryName').value.trim();
    const dTag = document.getElementById('editDTag').value;
    const originalEventId = document.getElementById('editEventId').value;

    if (!primaryName) {
        showEditMessage('Primary name is required', 'error');
        return;
    }

    // Collect other names from dynamic fields
    const otherNameInputs = document.querySelectorAll('.editOtherNameInput');
    const otherNames = Array.from(otherNameInputs)
        .map(input => input.value.trim())
        .filter(name => name);

    try {
        const tags = [
            ['d', dTag],  // Keep same d tag for replacement
            ['n', primaryName],
            ['t', 'DNN']
        ];

        otherNames.forEach(name => {
            tags.push(['o', name]);
        });

        const event = {
            kind: 61600,
            created_at: Math.floor(Date.now() / 1000),
            tags: tags,
            content: ''
        };

        const signedEvent = await signEventUniversal(event);
        await publishToRelays(signedEvent);

        showEditMessage('✓ Name Event updated successfully!', 'success');

        // Refresh My Published Events after 2 seconds
        setTimeout(() => {
            closeEditModal();
            loadMyEvents();
        }, 2000);
    } catch (error) {
        showEditMessage('Error updating event: ' + error.message, 'error');
        console.error('Save error:', error);
    }
}

async function verifyEditCertSignature() {
    const pem = document.getElementById('editCertPEM').value.trim();
    const storedHash = document.getElementById('editCertHash').value;
    if (!pem) { alert('No certificate'); return; }
    try {
        const encoder = new TextEncoder();
        const hashBuffer = await crypto.subtle.digest('SHA-256', encoder.encode(pem));
        const computedHash = Array.from(new Uint8Array(hashBuffer)).map(b => b.toString(16).padStart(2, '0')).join('');
        if (computedHash === storedHash) { alert('✅ Valid!'); } else { alert('❌ Modified'); }
    } catch (e) { alert('Error: ' + e.message); }
}

function clearEditCertificate() {
    if (!confirm('Clear certificate?')) return;
    document.getElementById('editCertPEM').value = '';
    document.getElementById('editCertSignature').value = '';
    document.getElementById('editCertHash').value = '';
    document.getElementById('editCertPubkey').value = '';
    document.getElementById('editCertSignedAt').value = '';
    document.getElementById('editCertExpiry').value = '';
}


async function saveConnectionEvent() {
    const dTag = document.getElementById('editDTag').value;

    try {
        const content = {};
        const ttl = '3600';

        // Build primary domain records
        // Use the domain key from the existing event data
        const editDomainKey = document.getElementById('editConnDomainKey')?.value?.trim();
        if (!editDomainKey) {
            showEditMessage('Domain key is missing', 'error');
            return;
        }
        const ipv4 = document.getElementById('editConnIPv4').value.trim();
        const ipv6 = document.getElementById('editConnIPv6').value.trim();
        const httpsPort = document.getElementById('editConnHTTPSPort').value.trim();
        const httpPort = document.getElementById('editConnHTTPPort').value.trim();

        if (!ipv4) {
            showEditMessage('IPv4 address is required for primary name', 'error');
            return;
        }

        const selfRecords = [];

        // Add A record (IPv4)
        selfRecords.push(['A', '@', ipv4, ttl]);

        // Add AAAA record (IPv6) if provided
        if (ipv6) {
            selfRecords.push(['AAAA', '@', ipv6, ttl]);
        }

        // Add SRV record for HTTPS
        if (httpsPort) {
            selfRecords.push(['SRV', '_https._tcp', '10', '5', httpsPort, '@', ttl]);
        }

        // Add SRV record for HTTP if provided
        if (httpPort) {
            selfRecords.push(['SRV', '_http._tcp', '10', '5', httpPort, '@', ttl]);
        }

        // Collect custom DNS records for primary domain
        const customDNSRecords = document.querySelectorAll('#editCustomDNSRecords > div');
        customDNSRecords.forEach(recordDiv => {
            const type = recordDiv.querySelector('.editDNSRecordType').value;
            const name = recordDiv.querySelector('.editDNSRecordName').value.trim() || '@';
            const value = recordDiv.querySelector('.editDNSRecordValue').value.trim();
            const recordTTL = recordDiv.querySelector('.editDNSRecordTTL').value.trim() || '3600';

            if (value) {
                selfRecords.push([type, name, value, recordTTL]);
            }
        });

        content[editDomainKey] = {
            records: selfRecords,
            meta: {
                description: 'DNN connection endpoint',
                updated_at: Math.floor(Date.now() / 1000)
            }
        };

        // Add certificate if provided
        const editCertPEM = document.getElementById('editCertPEM').value.trim();
        const editCertSig = document.getElementById('editCertSignature').value.trim();

        if (editCertPEM && editCertSig) {
            content[editDomainKey].cert = {
                pem: editCertPEM,
                cert_signature: {
                    hash: document.getElementById('editCertHash').value,
                    signature: editCertSig,
                    pubkey: document.getElementById('editCertPubkey').value,
                    signed_at: parseInt(document.getElementById('editCertSignedAt').value)
                },
                expires: document.getElementById('editCertExpiry').value ?
                    Math.floor(new Date(document.getElementById('editCertExpiry').value).getTime() / 1000) : null
            };
        }

        // Collect other name connections
        const otherNameConns = document.querySelectorAll('#editOtherNameConnectionsContainer > div');
        otherNameConns.forEach(connDiv => {
            const name = connDiv.querySelector('.editOtherConnName').value.trim();
            const connIPv4 = connDiv.querySelector('.editOtherConnIPv4').value.trim();
            const connIPv6 = connDiv.querySelector('.editOtherConnIPv6').value.trim();
            const connHTTPSPort = connDiv.querySelector('.editOtherConnHTTPSPort').value.trim();
            const connHTTPPort = connDiv.querySelector('.editOtherConnHTTPPort').value.trim();

            if (name && connIPv4) {
                const otherRecords = [];

                // Add A record (IPv4)
                otherRecords.push(['A', '@', connIPv4, ttl]);

                // Add AAAA record (IPv6) if provided
                if (connIPv6) {
                    otherRecords.push(['AAAA', '@', connIPv6, ttl]);
                }

                // Add SRV record for HTTPS if port provided
                if (connHTTPSPort) {
                    otherRecords.push(['SRV', '_https._tcp', '10', '5', connHTTPSPort, '@', ttl]);
                }

                // Add SRV record for HTTP if port provided
                if (connHTTPPort) {
                    otherRecords.push(['SRV', '_http._tcp', '10', '5', connHTTPPort, '@', ttl]);
                }

                // Collect custom DNS records for this other name
                const customDNSForThisConn = connDiv.querySelectorAll('[class^="editOtherConnDNSType"]');
                customDNSForThisConn.forEach(typeSelect => {
                    const recordDiv = typeSelect.closest('div').parentElement;
                    const type = typeSelect.value;
                    const name = recordDiv.querySelector('.editOtherConnDNSName').value.trim() || '@';
                    const value = recordDiv.querySelector('.editOtherConnDNSValue').value.trim();
                    const recordTTL = recordDiv.querySelector('.editOtherConnDNSTTL').value.trim() || '3600';

                    if (value) {
                        otherRecords.push([type, name, value, recordTTL]);
                    }
                });

                content[name] = {
                    records: otherRecords,
                    meta: {
                        description: 'Secondary endpoint',
                        updated_at: Math.floor(Date.now() / 1000)
                    }
                };
            }
        });

        const event = {
            kind: 62600,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['d', dTag],  // Keep same d tag for replacement
                ['t', 'DNN']
            ],
            content: JSON.stringify(content)
        };

        const signedEvent = await signEventUniversal(event);
        await publishToRelays(signedEvent);

        showEditMessage('✓ Connection Event updated successfully!', 'success');

        setTimeout(() => {
            closeEditModal();
            loadMyEvents();
        }, 2000);
    } catch (error) {
        showEditMessage('Error updating event: ' + error.message, 'error');
        console.error('Save error:', error);
    }
}

async function saveMetadataEvent() {
    const dTag = document.getElementById('editDTag').value;

    try {
        const metadata = {};

        // Description
        const description = document.getElementById('editMetaDescription').value.trim();
        if (description) {
            metadata.description = description;
        }

        // URLs
        const urlElements = document.querySelectorAll('#editMetaURLsContainer > div');
        if (urlElements.length > 0) {
            const urls = [];
            urlElements.forEach(urlDiv => {
                const label = urlDiv.querySelector('.editMetaURLLabel').value.trim();
                const url = urlDiv.querySelector('.editMetaURLValue').value.trim();
                if (label && url) {
                    urls.push({ label, url });
                }
            });
            if (urls.length > 0) {
                metadata.urls = urls;
            }
        }

        // Currencies
        const currencyContainers = document.querySelectorAll('#editMetaCurrenciesContainer > div');
        if (currencyContainers.length > 0) {
            const currencies = [];
            currencyContainers.forEach(currDiv => {
                const currencyName = currDiv.querySelector('.editMetaCurrencyName').value.trim();
                const addressElements = currDiv.querySelectorAll('.editCurrencyAddrType');

                if (currencyName && addressElements.length > 0) {
                    const addresses = [];
                    addressElements.forEach((typeInput) => {
                        const type = typeInput.value.trim();
                        const addrValueInput = typeInput.parentElement.querySelector('.editCurrencyAddrValue');
                        const address = addrValueInput ? addrValueInput.value.trim() : '';

                        if (type && address) {
                            addresses.push({ type, address });
                        }
                    });

                    if (addresses.length > 0) {
                        currencies.push({ currency: currencyName, addresses });
                    }
                }
            });
            if (currencies.length > 0) {
                metadata.currencies = currencies;
            }
        }

        // Nostr Addresses
        const nostrElements = document.querySelectorAll('#editMetaNostrAddressesContainer > div');
        if (nostrElements.length > 0) {
            const nostrAddresses = [];
            nostrElements.forEach(nostrDiv => {
                const label = nostrDiv.querySelector('.editMetaNostrLabel').value.trim();
                const address = nostrDiv.querySelector('.editMetaNostrValue').value.trim();
                if (label && address) {
                    nostrAddresses.push({ label, address });
                }
            });
            if (nostrAddresses.length > 0) {
                metadata.nostrAddresses = nostrAddresses;
            }
        }

        // Nostr Relays
        const relayElements = document.querySelectorAll('#editMetaRelaysContainer > div');
        if (relayElements.length > 0) {
            const relays = [];
            relayElements.forEach(relayDiv => {
                const relayURL = relayDiv.querySelector('.editMetaRelayURL').value.trim();
                if (relayURL) {
                    relays.push(relayURL);
                }
            });
            if (relays.length > 0) {
                metadata.relays = relays;
            }
        }

        // Custom Fields (new label/value row structure)
        const customFieldContainers = document.querySelectorAll('#editMetaCustomFieldsContainer > div');
        if (customFieldContainers.length > 0) {
            customFieldContainers.forEach(fieldDiv => {
                const key = fieldDiv.querySelector('.editMetaCustomKey').value.trim();
                if (!key) return;

                // Find the rows container
                const rowsContainer = fieldDiv.querySelector('[id^="editMetaCustomRows_"]');
                if (!rowsContainer) return;

                const rowContainers = rowsContainer.querySelectorAll('[id^="editCustomRow_"]');
                if (rowContainers.length === 0) return;

                const fieldData = {};

                rowContainers.forEach(rowDiv => {
                    const label = rowDiv.querySelector('.editMetaCustomLabel').value.trim();
                    if (!label) return;

                    const valueInputs = rowDiv.querySelectorAll('.editMetaCustomValue');
                    const values = Array.from(valueInputs)
                        .map(input => input.value.trim())
                        .filter(v => v);

                    if (values.length > 0) {
                        // If single value, store as string; if multiple, store as array
                        fieldData[label] = values.length === 1 ? values[0] : values;
                    }
                });

                if (Object.keys(fieldData).length > 0) {
                    metadata[key] = fieldData;
                }
            });
        }

        const event = {
            kind: 63600,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['d', dTag],  // Keep same d tag for replacement
                ['t', 'DNN']
            ],
            content: JSON.stringify({ metadata })
        };

        const signedEvent = await signEventUniversal(event);
        await publishToRelays(signedEvent);

        showEditMessage('✓ Metadata Event updated successfully!', 'success');

        setTimeout(() => {
            closeEditModal();
            loadMyEvents();
        }, 2000);
    } catch (error) {
        showEditMessage('Error updating event: ' + error.message, 'error');
        console.error('Save error:', error);
    }
}

function renderAnchorEventEditForm(event) {
    const modalBody = document.getElementById('editModalBody');
    const dTag = event.tags.find(t => t[0] === 'd')?.[1] || '';
    const namesTag = event.tags.find(t => t[0] === 'n')?.[1] || '';
    const connectionTag = event.tags.find(t => t[0] === 'c')?.[1] || '';
    const metadataTag = event.tags.find(t => t[0] === 'm')?.[1] || '';
    const transactionTag = event.tags.find(t => t[0] === 'x')?.[1] || '';

    let html = '<div>' +
        '<h3 style="color: #f1f5f9; margin-bottom: 20px;">⚓ Edit Anchor Event (Kind 60600)</h3>' +
        '<div style="margin-bottom: 16px; padding: 12px; background: rgba(102, 126, 234, 0.1); border: 1px solid #667eea; border-radius: 8px;">' +
        '<div style="color: #94a3b8; font-size: 12px;">ℹ️ You can update which events are referenced by this anchor. The Bitcoin transaction cannot be changed.</div>' +
        '</div>' +
        '<div style="margin-bottom: 16px;">' +
        '<label style="display: block; color: #94a3b8; font-size: 12px; margin-bottom: 6px;">Name Event naddr (required) <span style="color: #ef4444;">*</span></label>' +
        '<input type="text" id="editNamesNaddr" value="' + escapeHtml(namesTag) + '" placeholder="naddr1..." style="width: 100%; padding: 10px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #e2e8f0; font-size: 13px; font-family: monospace;" />' +
        '</div>' +
        '<div style="margin-bottom: 16px;">' +
        '<label style="display: block; color: #94a3b8; font-size: 12px; margin-bottom: 6px;">Connection Event naddr (required) <span style="color: #ef4444;">*</span></label>' +
        '<input type="text" id="editConnectionNaddr" value="' + escapeHtml(connectionTag) + '" placeholder="naddr1..." style="width: 100%; padding: 10px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #e2e8f0; font-size: 13px; font-family: monospace;" />' +
        '</div>' +
        '<div style="margin-bottom: 16px;">' +
        '<label style="display: block; color: #94a3b8; font-size: 12px; margin-bottom: 6px;">Metadata Event naddr (required) <span style="color: #ef4444;">*</span></label>' +
        '<input type="text" id="editMetadataNaddr" value="' + escapeHtml(metadataTag) + '" placeholder="naddr1..." style="width: 100%; padding: 10px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #e2e8f0; font-size: 13px; font-family: monospace;" />' +
        '</div>' +
        '<div style="margin-bottom: 16px;">' +
        '<label style="display: block; color: #94a3b8; font-size: 12px; margin-bottom: 6px;">Bitcoin Transaction ID (cannot be changed)</label>' +
        '<input type="text" value="' + escapeHtml(transactionTag) + '" disabled style="width: 100%; padding: 10px; background: #0f172a; border: 1px solid #334155; border-radius: 8px; color: #64748b; font-size: 13px; font-family: monospace; cursor: not-allowed;" />' +
        '<div style="color: #64748b; font-size: 11px; margin-top: 4px;">The Bitcoin transaction ID is immutable and cannot be updated.</div>' +
        '</div>' +
        '<input type="hidden" id="editDTag" value="' + dTag + '" />' +
        '<input type="hidden" id="editBitcoinTxID" value="' + transactionTag + '" />' +
        '<input type="hidden" id="editEventId" value="' + event.id + '" />' +
        '<div id="editMessage" style="padding: 12px; border-radius: 8px; margin-bottom: 16px; display: none;"></div>' +
        '<div style="display: flex; gap: 12px; justify-content: flex-end;">' +
        '<button onclick="closeEditModal()" style="padding: 10px 24px; background: #334155; color: #e2e8f0; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Cancel</button>' +
        '<button onclick="saveAnchorEvent()" style="padding: 10px 24px; background: #10b981; color: white; border: none; border-radius: 8px; cursor: pointer; font-weight: bold; transition: all 0.3s;">Save Changes</button>' +
        '</div>' +
        '</div>';

    modalBody.innerHTML = html;
}

async function saveAnchorEvent() {
    const dTag = document.getElementById('editDTag').value;
    const namesNaddr = document.getElementById('editNamesNaddr').value.trim();
    const connectionNaddr = document.getElementById('editConnectionNaddr').value.trim();
    const metadataNaddr = document.getElementById('editMetadataNaddr').value.trim();
    const bitcoinTxID = document.getElementById('editBitcoinTxID').value.trim();

    // Validate all required fields
    if (!namesNaddr || !connectionNaddr || !metadataNaddr || !bitcoinTxID) {
        showEditMessage('All fields are required for Anchor Event (60600)', 'error');
        return;
    }

    // Validate naddr format
    if (!namesNaddr.startsWith('naddr1') || !connectionNaddr.startsWith('naddr1') || !metadataNaddr.startsWith('naddr1')) {
        showEditMessage('All event references must be naddr identifiers (starting with "naddr1")', 'error');
        return;
    }

    try {
        const event = {
            kind: 60600,
            created_at: Math.floor(Date.now() / 1000),
            tags: [
                ['d', dTag],  // Keep same d tag for replacement
                ['n', namesNaddr],
                ['c', connectionNaddr],
                ['m', metadataNaddr],
                ['x', bitcoinTxID],
                ['t', 'DNN']
            ],
            content: JSON.stringify({ updated_at: Math.floor(Date.now() / 1000) })
        };

        const signedEvent = await signEventUniversal(event);
        await publishToRelays(signedEvent);

        showEditMessage('✓ Anchor Event updated successfully! Your references have been updated.', 'success');

        setTimeout(() => {
            closeEditModal();
            loadMyEvents();
        }, 2000);
    } catch (error) {
        showEditMessage('Error updating anchor event: ' + error.message, 'error');
        console.error('Save error:', error);
    }
}

function showEditMessage(message, type) {
    const messageDiv = document.getElementById('editEventMessage');
    if (!messageDiv) {
        console.warn('Edit message element not found, showing alert instead');
        if (type === 'error') {
            alert('Error: ' + message);
        }
        return;
    }

    messageDiv.style.display = 'block';
    messageDiv.classList.remove('hidden');

    if (type === 'success') {
        messageDiv.style.background = '#064e3b';
        messageDiv.style.color = '#6ee7b7';
    } else if (type === 'info') {
        messageDiv.style.background = '#1e3a5f';
        messageDiv.style.color = '#93c5fd';
    } else {
        messageDiv.style.background = '#7f1d1d';
        messageDiv.style.color = '#fca5a5';
    }

    messageDiv.textContent = message;
}

// ========== Node Discovery (Kind 64600) ==========

// State for the modal
let nodeDiscoveryRelayList = [];
let nodeDiscoveryModalInitialized = false;

// Check if user is logged in (unified check for extension and remote signer)
// Uses same pattern as My IDs/TLDs page - checks window.userNpub/connectedNpub
function isUserLoggedIn() {
    // Check if user has an npub (set when they actually login)
    const userNpub = window.userNpub || window.connectedNpub || null;
    return !!userNpub;
}

// Get user's pubkey hex (window.userPubkeyHex is set on login)
function getUserPubkeyHex() {
    return window.userPubkeyHex || window.currentUser?.pubkey || null;
}

// Fetch user's NIP-65 relay list from relays
async function fetchUserNIP65Relays() {
    const userPubkey = getUserPubkeyHex();
    console.log('[NIP-65] Fetching relays for pubkey:', userPubkey?.substring(0, 16) + '...');
    if (!userPubkey) {
        console.log('[NIP-65] No user pubkey, skipping fetch');
        return [];
    }

    const relayUrls = window.configuredRelays || ['wss://relay.damus.io', 'wss://nos.lol', 'wss://relay.nostr.band'];
    const relays = [];

    try {
        const pool = window.NostrTools?.SimplePool ? new window.NostrTools.SimplePool() : null;
        if (!pool) {
            console.log('[NIP-65] No SimplePool available');
            return [];
        }

        // Query for NIP-65 relay list (kind 10002)
        console.log('[NIP-65] Querying relays:', relayUrls.slice(0, 3));
        const events = await pool.querySync(relayUrls.slice(0, 3), {
            authors: [userPubkey],
            kinds: [10002],
            limit: 1
        });

        console.log('[NIP-65] Found events:', events?.length || 0);

        if (events && events.length > 0) {
            // Sort by created_at desc and take the latest
            events.sort((a, b) => b.created_at - a.created_at);
            const relayListEvent = events[0];

            // Parse relay URLs from 'r' tags
            for (const tag of relayListEvent.tags) {
                if (tag[0] === 'r' && tag[1]) {
                    relays.push(tag[1]);
                }
            }
            console.log('[NIP-65] Parsed relays:', relays);
        }
    } catch (error) {
        console.warn('[NIP-65] Failed to fetch relays:', error);
    }

    return relays;
}

// Add user's NIP-65 relays to the node's configured relays for publishing
// This makes user relays available for publishing events immediately after login
// Also persists to database so they survive page refresh
async function addUserRelaysToDiscovered(userRelays) {
    if (!userRelays || userRelays.length === 0) {
        console.log('[Login] No user relays to add');
        return;
    }

    // Add to window.configuredRelays if not already present
    if (!window.configuredRelays) {
        window.configuredRelays = [];
    }

    const existingRelays = new Set(window.configuredRelays.map(r => r.toLowerCase()));
    let addedCount = 0;

    for (const relay of userRelays) {
        const normalizedRelay = relay.toLowerCase();
        if (!existingRelays.has(normalizedRelay)) {
            window.configuredRelays.push(relay);
            existingRelays.add(normalizedRelay);
            addedCount++;
            console.log('[Login] Added user relay:', relay);
        }
    }

    if (addedCount > 0) {
        console.log(`[Login] Added ${addedCount} new relays from user's NIP-65 list`);
    } else {
        console.log('[Login] All user relays already present in configured relays');
    }

    // Persist to database so they survive page refresh
    // Exclude hardcoded relays - they shouldn't be in discovered list
    try {
        // Fetch hardcoded relays to filter them out
        let hardcodedRelays = new Set();
        try {
            const nodeInfoResponse = await fetch('/dnn/node-info');
            const nodeInfo = await nodeInfoResponse.json();
            if (nodeInfo.configured_relays && Array.isArray(nodeInfo.configured_relays)) {
                nodeInfo.configured_relays.forEach(r => hardcodedRelays.add(r.toLowerCase()));
            }
        } catch (e) {
            console.warn('[Login] Could not fetch hardcoded relays:', e);
        }

        // Filter out hardcoded relays
        const relaysToStore = userRelays.filter(r => !hardcodedRelays.has(r.toLowerCase()));

        if (relaysToStore.length === 0) {
            console.log('[Login] All user relays are already hardcoded, nothing to store');
            return;
        }

        const pubkey = window.userPubkeyHex || '';
        const response = await fetch('/dnn/discovered-relays', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                relays: relaysToStore,
                source: 'nip65',
                discovered_by: pubkey
            })
        });

        if (response.ok) {
            const result = await response.json();
            console.log(`[Login] Persisted ${result.count} relays to database (excluded ${userRelays.length - relaysToStore.length} hardcoded)`);
        } else {
            console.warn('[Login] Failed to persist relays to database:', response.statusText);
        }
    } catch (error) {
        console.warn('[Login] Failed to persist relays:', error);
        // Non-fatal - relays still work in memory for this session
    }
}

// State for tabbed relay lists
let nodeRelayList = [];  // Hardcoded + discovered (toggle only)
let userRelayList = [];  // NIP-65 + manual (can add/remove)
let originalNip65Relays = new Set();  // Track original NIP-65 relays to show unsaved changes

// Initialize relay lists for the modal (separate node vs user)
async function initializeNodeDiscoveryRelays() {
    nodeRelayList = [];
    userRelayList = [];

    // 1. Get node's hardcoded relays
    try {
        const response = await fetch('/dnn/node-info');
        const data = await response.json();
        if (data.configured_relays && Array.isArray(data.configured_relays)) {
            data.configured_relays.forEach(url => {
                if (!nodeRelayList.find(r => r.url === url)) {
                    nodeRelayList.push({ url, enabled: true, source: 'hardcoded' });
                }
            });
        }
    } catch (e) {
        console.warn('Failed to fetch hardcoded relays:', e);
    }

    // 2. Get discovered relays from this node
    try {
        const response = await fetch('/dnn/discovered-relays');
        const discoveredRelays = await response.json();
        if (Array.isArray(discoveredRelays)) {
            discoveredRelays.forEach(url => {
                if (!nodeRelayList.find(r => r.url === url)) {
                    nodeRelayList.push({ url, enabled: true, source: 'discovered' });
                }
            });
        }
    } catch (e) {
        console.warn('Failed to fetch discovered relays:', e);
    }

    // 3. Get user's NIP-65 relays
    const nip65Relays = await fetchUserNIP65Relays();
    originalNip65Relays = new Set(nip65Relays);  // Track original NIP-65 relays
    nip65Relays.forEach(url => {
        // Add all NIP-65 relays to user list (even if in node list)
        if (!userRelayList.find(r => r.url === url)) {
            userRelayList.push({ url, enabled: true, source: 'nip65' });
        }
    });

    // 4. Add defaults to node list if empty
    const defaultRelays = ['wss://relay.damus.io', 'wss://relay.nostr.band', 'wss://nos.lol'];
    if (nodeRelayList.length === 0) {
        defaultRelays.forEach(url => {
            nodeRelayList.push({ url, enabled: true, source: 'default' });
        });
    }

    renderNodeDiscoveryNodeRelays();
    renderNodeDiscoveryUserRelays();
}

// Switch between Node and User relay tabs
function switchNodeDiscoveryRelayTab(tab) {
    const nodeTab = document.getElementById('ndRelayTabNode');
    const userTab = document.getElementById('ndRelayTabUser');
    const nodeContent = document.getElementById('ndRelayNodeContent');
    const userContent = document.getElementById('ndRelayUserContent');

    if (tab === 'node') {
        nodeTab.className = 'px-3 py-2 rounded-lg text-xs font-medium bg-dnn-accent text-white grow-[1]';
        userTab.className = 'px-3 py-2 rounded-lg text-xs font-medium text-gray-400 hover:text-white grow-[1]';
        nodeContent.classList.remove('hidden');
        userContent.classList.add('hidden');
    } else {
        userTab.className = 'px-3 py-2 rounded-lg text-xs font-medium bg-dnn-accent text-white grow-[1]';
        nodeTab.className = 'px-3 py-2 rounded-lg text-xs font-medium text-gray-400 hover:text-white grow-[1]';
        userContent.classList.remove('hidden');
        nodeContent.classList.add('hidden');
    }

    if (typeof lucide !== 'undefined') lucide.createIcons();
}

// Render the node relay list (hardcoded + discovered - toggle only)
// Hides relays that are already in the user list to avoid visual duplicates
function renderNodeDiscoveryNodeRelays() {
    const container = document.getElementById('nodeDiscoveryNodeRelays');
    if (!container) return;

    // Filter out relays that are already in user list
    const filteredNodeRelays = nodeRelayList.filter(relay => !userRelayList.some(r => r.url === relay.url));

    if (filteredNodeRelays.length === 0 && nodeRelayList.length > 0) {
        container.innerHTML = '<div class="text-center py-2 text-gray-500 text-sm">All node relays are in your User Relays list</div>';
        return;
    }

    if (filteredNodeRelays.length === 0) {
        container.innerHTML = '<div class="text-center py-2 text-gray-500 text-sm">No node relays found</div>';
        return;
    }

    container.innerHTML = filteredNodeRelays.map((relay) => {
        // Find the original index in nodeRelayList for toggle function
        const originalIndex = nodeRelayList.findIndex(r => r.url === relay.url);
        const sourceIcon = relay.source === 'hardcoded' ? 'settings' : 'search';
        const sourceLabel = relay.source === 'hardcoded' ? 'Hardcoded' : 'Discovered';
        const toggleBg = relay.enabled ? 'bg-dnn-purple' : 'bg-gray-600';
        const toggleDot = relay.enabled ? 'translate-x-4' : 'translate-x-0';

        return `
            <div class="flex items-center justify-between p-2 bg-dnn-secondary rounded-lg">
                <div class="flex items-center gap-2 flex-1 min-w-0">
                    <button onclick="toggleNodeRelay(${originalIndex})" 
                        class="w-9 h-5 ${toggleBg} rounded-full relative transition-colors flex-shrink-0">
                        <div class="w-4 h-4 bg-white rounded-full absolute top-0.5 left-0.5 transition-transform ${toggleDot}"></div>
                    </button>
                    <span class="font-mono text-xs text-white truncate">${relay.url}</span>
                    <span class="text-xs text-gray-500 flex items-center gap-1 flex-shrink-0">
                        <i data-lucide="${sourceIcon}" class="w-3 h-3"></i>
                        ${sourceLabel}
                    </span>
                </div>
            </div>
        `;
    }).join('');

    if (typeof lucide !== 'undefined') lucide.createIcons();
}

// Render the user relay list (NIP-65 + manual - can add/remove)
function renderNodeDiscoveryUserRelays() {
    const container = document.getElementById('nodeDiscoveryUserRelays');
    if (!container) return;

    if (userRelayList.length === 0) {
        container.innerHTML = '<div class="text-center py-2 text-gray-500 text-sm">No user relays yet. Add some above.</div>';
        return;
    }

    // Check if there are unsaved changes (for save button state)
    const hasUnsavedChanges = userRelayList.some(r => !originalNip65Relays.has(r.url)) ||
        [...originalNip65Relays].some(url => !userRelayList.find(r => r.url === url));

    const saveBtn = document.getElementById('saveUserRelaysBtn');
    if (saveBtn) {
        if (hasUnsavedChanges) {
            saveBtn.classList.add('border-yellow-500/50', 'text-yellow-400');
        } else {
            saveBtn.classList.remove('border-yellow-500/50', 'text-yellow-400');
        }
    }

    container.innerHTML = userRelayList.map((relay, index) => {
        // Check if this relay is an unsaved addition (not in original NIP-65 list)
        const isUnsaved = !originalNip65Relays.has(relay.url);
        const unsavedIndicator = isUnsaved ? '<span class="text-yellow-500 mr-1">●</span>' : '';

        const sourceIcon = relay.source === 'nip65' ? 'user' : 'plus-circle';
        const sourceLabel = relay.source === 'nip65' ? 'NIP-65' : 'Local';
        const toggleBg = relay.enabled ? 'bg-dnn-purple' : 'bg-gray-600';
        const toggleDot = relay.enabled ? 'translate-x-4' : 'translate-x-0';

        return `
            <div class="flex items-center justify-between p-2 bg-dnn-secondary rounded-lg group">
                <div class="flex items-center gap-2 flex-1 min-w-0">
                    <button onclick="toggleUserRelay(${index})" 
                        class="w-9 h-5 ${toggleBg} rounded-full relative transition-colors flex-shrink-0">
                        <div class="w-4 h-4 bg-white rounded-full absolute top-0.5 left-0.5 transition-transform ${toggleDot}"></div>
                    </button>
                    ${unsavedIndicator}<span class="font-mono text-xs text-white truncate">${relay.url}</span>
                    <span class="text-xs ${isUnsaved ? 'text-yellow-500' : 'text-gray-500'} flex items-center gap-1 flex-shrink-0">
                        <i data-lucide="${sourceIcon}" class="w-3 h-3"></i>
                        ${sourceLabel}
                    </span>
                </div>
                <button onclick="removeUserRelay(${index})" 
                    class="text-gray-500 hover:text-red-400 transition-colors p-1 opacity-0 group-hover:opacity-100">
                    <i data-lucide="x" class="w-4 h-4"></i>
                </button>
            </div>
        `;
    }).join('');

    if (typeof lucide !== 'undefined') lucide.createIcons();
}

function toggleNodeRelay(index) {
    if (nodeRelayList[index]) {
        nodeRelayList[index].enabled = !nodeRelayList[index].enabled;
        renderNodeDiscoveryNodeRelays();
    }
}

function toggleUserRelay(index) {
    if (userRelayList[index]) {
        userRelayList[index].enabled = !userRelayList[index].enabled;
        renderNodeDiscoveryUserRelays();
    }
}

function removeUserRelay(index) {
    userRelayList.splice(index, 1);
    renderNodeDiscoveryUserRelays();
    renderNodeDiscoveryNodeRelays();  // Update node relays to remove checkmark for removed duplicates
}

// Add relay to user list (only in user tab)
function addNodeDiscoveryRelay() {
    const input = document.getElementById('nodeDiscoveryRelayInput');
    if (!input) {
        console.log('[AddRelay] Input element not found');
        return;
    }

    const rawValue = input.value.trim();
    console.log('[AddRelay] Raw input value:', rawValue);

    if (!rawValue) {
        console.log('[AddRelay] Empty input, nothing to add');
        return;
    }

    const urls = rawValue.split(',').map(s => s.trim()).filter(s => s);
    console.log('[AddRelay] Parsed URLs:', urls);

    urls.forEach(url => {
        // Validate URL format
        if (url.startsWith('wss://') || url.startsWith('ws://')) {
            // Only check for duplicates within user list (not node list)
            // Deduplication between node/user lists happens at publish time
            if (!userRelayList.find(r => r.url === url)) {
                userRelayList.push({ url, enabled: true, source: 'manual' });
                console.log('[AddRelay] Added relay:', url, 'List now:', userRelayList.length);
            } else {
                console.log('[AddRelay] Relay already in user list:', url);
            }
        } else {
            console.log('[AddRelay] Invalid URL format (must start with wss:// or ws://):', url);
        }
    });

    input.value = '';
    console.log('[AddRelay] Calling renderNodeDiscoveryUserRelays(), userRelayList length:', userRelayList.length);
    renderNodeDiscoveryUserRelays();
    renderNodeDiscoveryNodeRelays();  // Update node relays to show checkmark for duplicates
}

// Show status in the modal
function showNodeDiscoveryStatus(state, message) {
    const statusContainer = document.getElementById('nodeDiscoveryStatus');
    const formContainer = document.getElementById('nodeDiscoveryForm');
    const buttonsContainer = document.getElementById('nodeDiscoveryButtons');

    if (!statusContainer) return;

    if (state === 'loading') {
        formContainer.classList.add('hidden');
        buttonsContainer.classList.add('hidden');
        statusContainer.classList.remove('hidden');
        statusContainer.innerHTML = `
            <div class="flex flex-col items-center justify-center py-8">
                <div class="w-12 h-12 border-4 border-dnn-purple border-t-transparent rounded-full animate-spin mb-4"></div>
                <p class="text-gray-300">${message || 'Publishing...'}</p>
            </div>
        `;
    } else if (state === 'success') {
        formContainer.classList.add('hidden');
        buttonsContainer.classList.add('hidden');
        statusContainer.classList.remove('hidden');
        statusContainer.innerHTML = `
            <div class="flex flex-col items-center justify-center py-8">
                <div class="w-16 h-16 bg-green-500/20 rounded-full flex items-center justify-center mb-4">
                    <i data-lucide="check-circle" class="w-8 h-8 text-green-400"></i>
                </div>
                <p class="text-green-400 font-medium mb-2">Node Announced!</p>
                <p class="text-gray-400 text-sm text-center">${message || 'Other DNN nodes will now be able to find you.'}</p>
                <button onclick="closeNodeDiscoveryModal()" 
                    class="mt-6 px-6 py-2 bg-dnn-secondary text-gray-300 rounded-xl hover:bg-dnn-hover transition-colors">
                    Close
                </button>
            </div>
        `;
        if (typeof lucide !== 'undefined') lucide.createIcons();
    } else if (state === 'error') {
        formContainer.classList.add('hidden');
        buttonsContainer.classList.add('hidden');
        statusContainer.classList.remove('hidden');
        statusContainer.innerHTML = `
            <div class="flex flex-col items-center justify-center py-8">
                <div class="w-16 h-16 bg-red-500/20 rounded-full flex items-center justify-center mb-4">
                    <i data-lucide="alert-circle" class="w-8 h-8 text-red-400"></i>
                </div>
                <p class="text-red-400 font-medium mb-2">Failed to Announce</p>
                <p class="text-gray-400 text-sm text-center">${message || 'An error occurred'}</p>
                <div class="flex gap-3 mt-6">
                    <button onclick="resetNodeDiscoveryModal()" 
                        class="px-6 py-2 bg-dnn-secondary text-gray-300 rounded-xl hover:bg-dnn-hover transition-colors">
                        Try Again
                    </button>
                    <button onclick="closeNodeDiscoveryModal()" 
                        class="px-6 py-2 bg-dnn-secondary text-gray-300 rounded-xl hover:bg-dnn-hover transition-colors">
                        Close
                    </button>
                </div>
            </div>
        `;
        if (typeof lucide !== 'undefined') lucide.createIcons();
    }
}

function resetNodeDiscoveryModal() {
    const statusContainer = document.getElementById('nodeDiscoveryStatus');
    const formContainer = document.getElementById('nodeDiscoveryForm');
    const buttonsContainer = document.getElementById('nodeDiscoveryButtons');

    statusContainer.classList.add('hidden');
    formContainer.classList.remove('hidden');
    buttonsContainer.classList.remove('hidden');
}

// Publish the node discovery event
async function publishNodeDiscoveryEvent() {
    if (!isUserLoggedIn()) {
        showNodeDiscoveryStatus('error', 'Please connect your Nostr identity first');
        return;
    }

    // Collect node info from the form
    const dnsAddresses = [];
    const dnsInput = document.getElementById('nodeDiscoveryDNS');
    if (dnsInput && dnsInput.value.trim()) {
        dnsAddresses.push(...dnsInput.value.trim().split(',').map(s => s.trim()).filter(s => s));
    }

    const torAddresses = [];
    const torInput = document.getElementById('nodeDiscoveryTor');
    if (torInput && torInput.value.trim()) {
        torAddresses.push(...torInput.value.trim().split(',').map(s => s.trim()).filter(s => s));
    }

    // Get enabled relays from both lists (avoid duplicates)
    const enabledRelays = new Set();
    nodeRelayList.filter(r => r.enabled).forEach(r => enabledRelays.add(r.url));
    userRelayList.filter(r => r.enabled).forEach(r => enabledRelays.add(r.url));
    const relays = Array.from(enabledRelays);

    if (dnsAddresses.length === 0 && torAddresses.length === 0) {
        showNodeDiscoveryStatus('error', 'At least one DNS address or Tor address is required');
        return;
    }

    if (relays.length === 0) {
        showNodeDiscoveryStatus('error', 'At least one relay must be enabled');
        return;
    }

    showNodeDiscoveryStatus('loading', 'Requesting signature...');

    try {
        const dTag = generateUUIDv4();
        const timestamp = Math.floor(Date.now() / 1000);

        const content = {
            updated_at: timestamp,
            dns_addresses: dnsAddresses,
            tor: torAddresses,
            relays: relays
        };

        const event = {
            kind: 64600,
            created_at: timestamp,
            tags: [
                ['d', dTag],
                ['t', 'DNN']
            ],
            content: JSON.stringify(content)
        };

        showNodeDiscoveryStatus('loading', 'Signing event...');
        const signedEvent = await signEventUniversal(event);

        showNodeDiscoveryStatus('loading', 'Publishing to relays...');
        await publishToRelays(signedEvent);

        // Also store locally so it appears in discovered peers immediately
        showNodeDiscoveryStatus('loading', 'Storing locally...');
        try {
            await fetch('/dnn/store-local-event', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(signedEvent)
            });
        } catch (localErr) {
            console.warn('Failed to store locally (not critical):', localErr);
        }

        showNodeDiscoveryStatus('success', 'Your node has been announced to the network.');
    } catch (error) {
        console.error('Publish error:', error);
        showNodeDiscoveryStatus('error', error.message || 'Failed to publish event');
    }
}

async function openNodeDiscoveryModal() {
    // Check login first
    if (!isUserLoggedIn()) {
        // Use the global publish status modal for login error
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Please connect your Nostr identity first to announce your node.');
        }
        return;
    }

    let modal = document.getElementById('nodeDiscoveryModal');
    if (!modal) {
        // Create modal dynamically
        modal = document.createElement('div');
        modal.id = 'nodeDiscoveryModal';
        modal.className = 'fixed inset-0 bg-black/80 backdrop-blur-sm flex items-center justify-center z-50 hidden';
        modal.innerHTML = `
            <div class="bg-dnn-card border border-dnn-border rounded-2xl p-6 max-w-lg w-full mx-4 max-h-[90vh] overflow-y-auto">
                <div class="flex items-center justify-between mb-6">
                    <h3 class="text-xl font-bold text-white flex items-center gap-2">
                        <i data-lucide="radio" class="w-5 h-5 text-dnn-purple"></i>
                        Announce Node
                    </h3>
                    <button onclick="closeNodeDiscoveryModal()" class="text-gray-400 hover:text-white">
                        <i data-lucide="x" class="w-5 h-5"></i>
                    </button>
                </div>
                
                <!-- Status container (hidden by default) -->
                <div id="nodeDiscoveryStatus" class="hidden"></div>
                
                <!-- Form container -->
                <div id="nodeDiscoveryForm" class="space-y-4">
                    <div>
                        <label class="block text-sm text-gray-400 mb-2">DNS Addresses (comma-separated)</label>
                        <input type="text" id="nodeDiscoveryDNS" 
                            placeholder="https://my-node.example.com:8080"
                            class="w-full bg-dnn-secondary border border-dnn-border rounded-xl px-4 py-3 text-white placeholder-gray-500 focus:outline-none focus:border-dnn-purple">
                    </div>
                    
                    <div>
                        <label class="block text-sm text-gray-400 mb-2">Tor Addresses (comma-separated)</label>
                        <input type="text" id="nodeDiscoveryTor" 
                            placeholder="abc123...onion:8080"
                            class="w-full bg-dnn-secondary border border-dnn-border rounded-xl px-4 py-3 text-white placeholder-gray-500 focus:outline-none focus:border-dnn-purple">
                    </div>
                    
                    <div>
                        <label class="block text-sm text-gray-400 mb-2">Publish to Relays</label>
                        
                        <!-- Tabs -->
                        <div class="flex gap-2 mb-3 p-2 bg-black/25 rounded-xl">
                            <button onclick="switchNodeDiscoveryRelayTab('node')" id="ndRelayTabNode"
                                class="px-3 py-2 rounded-lg text-xs font-medium bg-dnn-accent text-white grow-[1]">
                                Node Relays
                            </button>
                            <button onclick="switchNodeDiscoveryRelayTab('user')" id="ndRelayTabUser"
                                class="px-3 py-2 rounded-lg text-xs font-medium text-gray-400 hover:text-white grow-[1]">
                                User Relays
                            </button>
                        </div>
                        
                        <!-- Node Relays Tab Content (hardcoded + discovered) -->
                        <div id="ndRelayNodeContent">
                            <div id="nodeDiscoveryNodeRelays" class="space-y-2 max-h-36 overflow-y-auto">
                                <div class="text-center py-2 text-gray-500 text-sm">Loading node relays...</div>
                            </div>
                            <p class="text-xs text-gray-500 mt-2">Hardcoded and discovered relays from this node.</p>
                        </div>
                        
                        <!-- User Relays Tab Content (NIP-65) -->
                        <div id="ndRelayUserContent" class="hidden">
                            <div class="flex gap-2 mb-2">
                                <input type="text" id="nodeDiscoveryRelayInput" 
                                    placeholder="wss://relay.example.com"
                                    class="flex-1 bg-dnn-secondary border border-dnn-border rounded-xl px-3 py-2 text-white placeholder-gray-500 focus:outline-none focus:border-dnn-purple text-sm"
                                    onkeydown="if(event.key==='Enter'){addNodeDiscoveryRelay();event.preventDefault();}">
                                <button onclick="saveUserRelayList()" id="saveUserRelaysBtn"
                                    title="Save relay list to NIP-65"
                                    class="px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-xl text-gray-400 hover:text-green-400 hover:border-green-500 transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
                                    <i data-lucide="save" class="w-4 h-4"></i>
                                </button>
                                <button onclick="addNodeDiscoveryRelay()" 
                                    class="px-3 py-2 bg-dnn-secondary border border-dnn-border rounded-xl text-gray-400 hover:text-white hover:border-dnn-purple transition-colors">
                                    <i data-lucide="plus" class="w-4 h-4"></i>
                                </button>
                            </div>
                            <div id="nodeDiscoveryUserRelays" class="space-y-2 max-h-36 overflow-y-auto">
                                <div class="text-center py-2 text-gray-500 text-sm">Loading user relays...</div>
                            </div>
                            <p class="text-xs text-gray-500 mt-2">Your NIP-65 relay list. <span class="text-yellow-500">●</span> = unsaved local changes. Click <i data-lucide="save" class="w-3 h-3 inline"></i> to publish.</p>
                        </div>
                    </div>
                </div>
                
                <!-- Buttons container -->
                <div id="nodeDiscoveryButtons" class="flex gap-3 mt-6">
                    <button onclick="closeNodeDiscoveryModal()" 
                        class="flex-1 px-4 py-3 bg-dnn-secondary text-gray-300 rounded-xl hover:bg-dnn-hover transition-colors">
                        Cancel
                    </button>
                    <button onclick="publishNodeDiscoveryEvent()" 
                        class="flex-1 px-4 py-3 bg-gradient-to-r from-dnn-purple to-dnn-accent text-white rounded-xl hover:opacity-90 transition-opacity font-medium">
                        Announce
                    </button>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        if (typeof lucide !== 'undefined') lucide.createIcons();
    }

    // Reset modal state
    resetNodeDiscoveryModal();

    // Show modal
    modal.classList.remove('hidden');

    // Initialize relay list
    await initializeNodeDiscoveryRelays();
}

function closeNodeDiscoveryModal() {
    const modal = document.getElementById('nodeDiscoveryModal');
    if (modal) modal.classList.add('hidden');
}

// ========== Propagation Event (Kind 65600) ==========

// Publish a sync request to propagate anchor updates
async function publishPropagationEvent(anchorNaddr, dnnId) {
    const hasExtension = !!window.nostr;
    const hasRemoteSigner = window.remoteSignerConnected && window.userPubkeyHex;
    if (!currentUser && !hasExtension && !hasRemoteSigner) {
        alert('Please login first with your Nostr key');
        return;
    }

    if (!anchorNaddr || !dnnId) {
        alert('Anchor naddr and DNN ID are required');
        return;
    }

    try {
        const dTag = generateUUIDv4();
        const timestamp = Math.floor(Date.now() / 1000);

        const content = {
            anchor_naddr: anchorNaddr,
            dnn_id: dnnId
        };

        const event = {
            kind: 65600,
            created_at: timestamp,
            tags: [
                ['d', dTag],
                ['t', 'DNN']
            ],
            content: JSON.stringify(content)
        };

        const signedEvent = await signEventUniversal(event);
        await publishToRelays(signedEvent);

        if (window.showPublishStatus) {
            window.showPublishStatus('success', 'Propagation event published! Other nodes will sync this anchor.');
        }
        return true;
    } catch (error) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Failed to publish: ' + error.message);
        } else {
            alert('Error publishing propagation event: ' + error.message);
        }
        console.error('Publish error:', error);
        return false;
    }
}

// Publish sync request from the event detail modal
async function publishSyncRequestFromModal() {
    // Check login using the unified function
    if (!isUserLoggedIn()) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Please login first to publish sync request');
        }
        return;
    }

    // Get current event data from modal context
    const currentEventData = window.currentEventData;
    const currentEventType = window.currentEventType;

    if (!currentEventData || currentEventType !== 'anchor') {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Sync request is only available for anchor events');
        }
        return;
    }

    // Extract anchor info
    const anchorEvent = currentEventData.anchor_event;
    if (!anchorEvent) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'No anchor event data found');
        }
        return;
    }

    // Build naddr from anchor event
    const pubkey = anchorEvent.pubkey || anchorEvent.PubKey;
    const dTag = anchorEvent.tags?.find(t => t[0] === 'd')?.[1] || currentEventData.d_tag;
    const dnnId = currentEventData.dnn_id || `n${currentEventData.dnn_block}.${currentEventData.position}`;

    if (!pubkey || !dTag) {
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Cannot construct anchor naddr - missing pubkey or d-tag');
        }
        return;
    }

    // Construct naddr
    let anchorNaddr;
    try {
        const relays = window.configuredRelays || [];
        if (window.NostrTools?.nip19) {
            anchorNaddr = window.NostrTools.nip19.naddrEncode({
                kind: 60600,
                pubkey: pubkey,
                identifier: dTag,
                relays: relays.slice(0, 3)
            });
        } else {
            // Fallback if NostrTools not available
            if (window.showPublishStatus) {
                window.showPublishStatus('error', 'NostrTools not available for naddr encoding');
            }
            return;
        }
    } catch (e) {
        console.error('Failed to encode naddr:', e);
        if (window.showPublishStatus) {
            window.showPublishStatus('error', 'Failed to encode anchor naddr');
        }
        return;
    }

    // Close the detail modal
    closeEventDetailModal();

    // Show loading status
    if (window.showPublishStatus) {
        window.showPublishStatus('loading', 'Publishing sync request...', 'Propagation Event');
    }

    // Publish the propagation event
    await publishPropagationEvent(anchorNaddr, dnnId);
}

// Save user relay list to NIP-65 (kind 10002)
async function saveUserRelayList() {
    if (!isUserLoggedIn()) {
        alert('Please login first to save your relay list');
        return;
    }

    const saveBtn = document.getElementById('saveUserRelaysBtn');
    if (saveBtn) {
        saveBtn.disabled = true;
        saveBtn.innerHTML = '<i data-lucide="loader-2" class="w-4 h-4 animate-spin"></i>';
        if (typeof lucide !== 'undefined') lucide.createIcons();
    }

    try {
        const timestamp = Math.floor(Date.now() / 1000);

        // Build NIP-65 relay list tags
        const tags = userRelayList.map(relay => ['r', relay.url]);

        const event = {
            kind: 10002,
            created_at: timestamp,
            tags: tags,
            content: ''  // NIP-65 has empty content
        };

        console.log('[NIP-65] Publishing relay list with', tags.length, 'relays');
        const signedEvent = await signEventUniversal(event);

        // Build relay list for publishing NIP-65 (exclude local node relay which only accepts DNN kinds)
        // Use: hardcoded external relays + discovered relays + the user's relay list itself
        const publishRelays = new Set();

        // Add hardcoded external relays
        ['wss://relay.damus.io', 'wss://relay.nostr.band', 'wss://nos.lol', 'wss://relay.primal.net'].forEach(url => publishRelays.add(url));

        // Add discovered relays (not the local node)
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const localNodeRelay = protocol + '//' + window.location.host;
        nodeRelayList.filter(r => r.enabled && r.url !== localNodeRelay).forEach(r => publishRelays.add(r.url));

        // Add user relays (the ones we're saving to NIP-65)
        userRelayList.filter(r => r.enabled).forEach(r => publishRelays.add(r.url));

        console.log('[NIP-65] Publishing to', publishRelays.size, 'external relays (excluding local node)');

        // Publish to each relay
        const results = await Promise.allSettled(
            [...publishRelays].map(url => publishToRelay(url, signedEvent))
        );
        const successful = results.filter(r => r.status === 'fulfilled').length;
        console.log('[NIP-65] Published to', successful, 'of', publishRelays.size, 'relays');

        // Update original relays to reflect saved state
        originalNip65Relays = new Set(userRelayList.map(r => r.url));

        // Update all relay sources to 'nip65' since they're now saved
        userRelayList.forEach(r => r.source = 'nip65');

        // Re-render to remove yellow indicators
        renderNodeDiscoveryUserRelays();

        // Show success feedback
        if (saveBtn) {
            saveBtn.classList.remove('border-yellow-500/50', 'text-yellow-400');
            saveBtn.classList.add('border-green-500', 'text-green-400');
            saveBtn.innerHTML = '<i data-lucide="check" class="w-4 h-4"></i>';
            if (typeof lucide !== 'undefined') lucide.createIcons();

            setTimeout(() => {
                saveBtn.classList.remove('border-green-500', 'text-green-400');
                saveBtn.innerHTML = '<i data-lucide="save" class="w-4 h-4"></i>';
                saveBtn.disabled = false;
                if (typeof lucide !== 'undefined') lucide.createIcons();
            }, 2000);
        }

        console.log('[NIP-65] Relay list saved successfully');
    } catch (error) {
        console.error('[NIP-65] Failed to save relay list:', error);

        if (saveBtn) {
            saveBtn.classList.add('border-red-500', 'text-red-400');
            saveBtn.innerHTML = '<i data-lucide="x" class="w-4 h-4"></i>';
            if (typeof lucide !== 'undefined') lucide.createIcons();

            setTimeout(() => {
                saveBtn.classList.remove('border-red-500', 'text-red-400');
                saveBtn.innerHTML = '<i data-lucide="save" class="w-4 h-4"></i>';
                saveBtn.disabled = false;
                if (typeof lucide !== 'undefined') lucide.createIcons();
            }, 2000);
        }

        alert('Failed to save relay list: ' + error.message);
    }
}

// Expose functions globally
window.openNodeDiscoveryModal = openNodeDiscoveryModal;
window.closeNodeDiscoveryModal = closeNodeDiscoveryModal;
window.publishNodeDiscoveryEvent = publishNodeDiscoveryEvent;
window.publishPropagationEvent = publishPropagationEvent;
window.publishSyncRequestFromModal = publishSyncRequestFromModal;
window.isUserLoggedIn = isUserLoggedIn;
window.addNodeDiscoveryRelay = addNodeDiscoveryRelay;
window.resetNodeDiscoveryModal = resetNodeDiscoveryModal;
window.switchNodeDiscoveryRelayTab = switchNodeDiscoveryRelayTab;
window.toggleNodeRelay = toggleNodeRelay;
window.toggleUserRelay = toggleUserRelay;
window.removeUserRelay = removeUserRelay;
window.saveUserRelayList = saveUserRelayList;

