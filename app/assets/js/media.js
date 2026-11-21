let atApi = null;
let currentBook = null;
const BOOK_PAGE_SIZE = 50000;

function setScoreModalVisible(show) {
    const modalEl = document.getElementById('scoreModal');
    let backdrop = document.getElementById('score-modal-backdrop');
    if (show) {
        modalEl.style.display = 'block';
        modalEl.removeAttribute('aria-hidden');
        modalEl.classList.add('show');
        document.body.classList.add('modal-open');
        if (!backdrop) {
            backdrop = document.createElement('div');
            backdrop.id = 'score-modal-backdrop';
            backdrop.className = 'modal-backdrop fade show';
            document.body.appendChild(backdrop);
        }
    } else {
        if (atApi) {
            try { atApi.stop(); } catch (_) {}
        }
        if (document.activeElement && modalEl.contains(document.activeElement)) document.activeElement.blur();
        modalEl.classList.remove('show');
        modalEl.style.display = 'none';
        modalEl.setAttribute('aria-hidden', 'true');
        document.body.classList.remove('modal-open');
        backdrop?.remove();
    }
}

async function loadBookPage(page) {
    if (!currentBook) return;
    const bodyEl = document.getElementById('articleModalBody');
    const pageInfo = document.getElementById('book-page-info');
    const prevBtn = document.getElementById('btn-book-prev');
    const nextBtn = document.getElementById('btn-book-next');

    currentBook.page = page;
    pageInfo.textContent = `${page + 1} / ${currentBook.totalPages}`;
    prevBtn.disabled = page <= 0;
    nextBtn.disabled = page >= currentBook.totalPages - 1;

    bodyEl.innerHTML = '<div class="text-center py-5"><div class="spinner-border text-primary" role="status"></div></div>';

    try {
        const offset = page * BOOK_PAGE_SIZE;
        const resp = await fetch(`/api/media?id=${encodeURIComponent(currentBook.id)}&offset=${offset}&length=${BOOK_PAGE_SIZE}`);
        if (!resp.ok) throw new Error('Failed to load page');
        const text = await resp.text();
        const safeText = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
        bodyEl.innerHTML = `<pre class="p-2" style="white-space: pre-wrap; word-break: break-word; font-family: inherit; font-size: 0.95rem;">${safeText}</pre>`;
        bodyEl.scrollTop = 0;
    } catch (_) {
        bodyEl.innerHTML = '<div class="alert alert-danger">Could not load book content.</div>';
    }
}

async function openBook(mediaId, title, totalLength) {
    const titleEl = document.getElementById('articleModalTitle');
    const bodyEl = document.getElementById('articleModalBody');
    const paginationEl = document.getElementById('articleModalPagination');

    titleEl.textContent = title || 'Book';
    bodyEl.innerHTML = '<div class="text-center py-5"><div class="spinner-border text-primary" role="status"></div></div>';
    setModalVisible(true);

    const length = totalLength > 0 ? totalLength : BOOK_PAGE_SIZE;
    const totalPages = Math.max(1, Math.ceil(length / BOOK_PAGE_SIZE));

    if (totalPages > 1) {
        paginationEl.style.display = 'flex';
        currentBook = { id: mediaId, title, totalPages, page: 0 };
        loadBookPage(0);
    } else {
        paginationEl.style.display = 'none';
        currentBook = null;
        try {
            const resp = await fetch(`/api/media?id=${encodeURIComponent(mediaId)}`);
            if (!resp.ok) throw new Error('Not found');
            const text = await resp.text();
            const safeText = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
            bodyEl.innerHTML = `<pre class="p-2" style="white-space: pre-wrap; word-break: break-word; font-family: inherit; font-size: 0.95rem;">${safeText}</pre>`;
        } catch (_) {
            bodyEl.innerHTML = '<div class="alert alert-danger">Could not load book content.</div>';
        }
    }
}

async function openScore(mediaId, title) {
    const titleEl = document.getElementById('scoreModalTitle');
    const container = document.getElementById('alphatab-container');
    const playBtn = document.getElementById('btn-score-play-pause');

    titleEl.textContent = title || 'Score';
    playBtn.innerHTML = '&#9654; Play';
    container.innerHTML = '<div class="text-center py-5"><div class="spinner-border text-primary" role="status"></div></div>';
    setScoreModalVisible(true);

    try {
        const resp = await fetch(`/api/media?id=${encodeURIComponent(mediaId)}`);
        if (!resp.ok) throw new Error('Failed to load score');
        const scoreData = await resp.arrayBuffer();

        container.innerHTML = '';

        if (atApi) {
            try { atApi.destroy(); } catch (_) {}
            atApi = null;
        }

        const settings = {
            core: {
                fontDirectory: '/fonts/',
                tracks: 'all'
            },
            player: {
                enablePlayer: true,
                soundFont: '/fonts/sonivox.sf2',
                scrollElement: document.getElementById('scoreModalBody')
            }
        };

        atApi = new alphaTab.AlphaTabApi(container, settings);

        atApi.scoreLoaded.on(score => {
            if (score && score.tracks && score.tracks.length > 0) {
                atApi.renderTracks(score.tracks);
            }
        });

        atApi.playerStateChanged.on(e => {
            if (e.state === 1) {
                playBtn.innerHTML = '&#9208; Pause';
            } else {
                playBtn.innerHTML = '&#9654; Play';
            }
        });

        atApi.load(new Uint8Array(scoreData));
    } catch (_) {
        container.innerHTML = '<div class="alert alert-danger m-3">Could not load score.</div>';
    }
}

document.addEventListener('DOMContentLoaded', () => {
    const closeScoreModal = () => setScoreModalVisible(false);
    document.getElementById('btn-score-modal-close')?.addEventListener('click', closeScoreModal);
    document.getElementById('btn-score-modal-close-x')?.addEventListener('click', closeScoreModal);
    document.getElementById('scoreModal')?.addEventListener('click', (e) => {
        if (e.target.id === 'scoreModal') closeScoreModal();
    });

    document.getElementById('btn-score-play-pause')?.addEventListener('click', () => {
        if (atApi) atApi.playPause();
    });

    document.getElementById('btn-score-stop')?.addEventListener('click', () => {
        if (atApi) atApi.stop();
    });

    document.getElementById('btn-book-prev')?.addEventListener('click', () => {
        if (currentBook && currentBook.page > 0) {
            loadBookPage(currentBook.page - 1);
        }
    });

    document.getElementById('btn-book-next')?.addEventListener('click', () => {
        if (currentBook && currentBook.page < currentBook.totalPages - 1) {
            loadBookPage(currentBook.page + 1);
        }
    });
});
