// The full-screen viewer: an album's photos one at a time on black, filling
// the screen, for showing an album on a phone (or a phone on a TV).
//
// A "Full screen" button (.viewer-open) carries the album's list URL, the
// photo to start on as JSON, and its position in the album.  Opening shows
// that photo at once, then fetches the list and fills in the rest, so the
// one tap that opens the viewer is also the gesture that's allowed to ask
// for real full screen.  Where the Fullscreen API is missing or refused,
// the viewer is a fixed overlay instead.
//
// Swiping is CSS scroll-snap: a horizontal track of slides, one per photo,
// so the browser does the touch physics.  Which slide is current is found
// by an IntersectionObserver.  Only slides near the current one have their
// images loaded.  The viewer pushes one history entry, so the back gesture
// closes it, and replaces the URL with each photo's page as it's shown.
// -- claude, 2026-10-21
(function () {
  'use strict';

  var FORMAT = 1;   // of photos.json
  var AROUND = 2;   // slides on each side of the current one to keep loaded
  var STATE  = { jiggleViewer: true };

  var viewer = null;

  function noop () {}

  function el (tag, className) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    return node;
  }

  function isFullscreen () {
    return !! (document.fullscreenElement || document.webkitFullscreenElement);
  }

  function enterFullscreen (node) {
    var request = node.requestFullscreen || node.webkitRequestFullscreen;
    if (! request) return;
    try {
      var p = request.call(node, { navigationUI: 'hide' });
      if (p && p.catch) p.catch(noop);
    } catch (e) {}
  }

  function exitFullscreen () {
    if (! isFullscreen()) return;
    var exit = document.exitFullscreen || document.webkitExitFullscreen;
    try {
      var p = exit.call(document);
      if (p && p.catch) p.catch(noop);
    } catch (e) {}
  }

  // The screen would otherwise dim and lock partway through showing an
  // album.  The lock is dropped when the page is hidden, so it's asked for
  // again when the page comes back.
  function keepAwake () {
    if (! navigator.wakeLock) return;
    navigator.wakeLock.request('screen').then(function (lock) {
      if (viewer) viewer.lock = lock;
      else lock.release().catch(noop);
    }).catch(noop);
  }

  function buildSlide (photo, i) {
    var slide = el('div', 'viewer-slide');
    slide.dataset.index = i;

    var img = el('img');
    img.alt = photo.title;
    img.sizes = '100vw';
    slide.appendChild(img);

    if (photo.video) {
      var play = el('button', 'viewer-play');
      play.type = 'button';
      play.setAttribute('aria-label', 'Play video');
      slide.appendChild(play);
    }

    return slide;
  }

  function loadAround (index) {
    var v = viewer;
    var from = Math.max(0, index - AROUND);
    var to   = Math.min(v.photos.length - 1, index + AROUND);

    for (var i = from; i <= to; i++) {
      var img = v.slides[i].querySelector('img');
      if (! img || img.src) continue;
      img.srcset = v.photos[i].srcset;
      img.src    = v.photos[i].src;
    }
  }

  function caption () {
    var v = viewer;
    var photo = v.photos[v.index];

    v.caption.textContent = '';
    var title = el('span', 'viewer-title');
    title.textContent = photo.title;
    v.caption.appendChild(title);

    if (v.complete) {
      var count = el('span', 'viewer-count');
      count.textContent = (v.index + 1) + ' of ' + v.photos.length;
      v.caption.appendChild(count);
    }
  }

  function stopVideo (v, index) {
    if (index < 0) return;
    var video = v.slides[index].querySelector('video');
    if (video) video.pause();
  }

  function playVideo (slide) {
    var photo = viewer.photos[ Number(slide.dataset.index) ];
    var video = el('video');
    video.controls    = true;
    video.playsInline = true;
    video.width  = photo.video.width;
    video.height = photo.video.height;
    video.poster = slide.querySelector('img').currentSrc || photo.src;
    video.src    = photo.video.url;

    slide.textContent = '';
    slide.appendChild(video);
    var p = video.play();
    if (p && p.catch) p.catch(noop);
  }

  function show (index) {
    var v = viewer;
    if (index === v.index) return;
    stopVideo(v, v.index);
    v.index = index;
    loadAround(index);
    caption();
    history.replaceState(STATE, '', v.photos[index].url);
  }

  function step (delta) {
    var v = viewer;
    var i = v.index + delta;
    if (i < 0 || i >= v.photos.length) return;
    v.track.scrollTo({ left: i * v.track.clientWidth, behavior: 'smooth' });
  }

  // Lay out the track for these photos, with the given one in view.
  function fill (photos, index, complete) {
    var v = viewer;
    if (v.observer) v.observer.disconnect();

    v.photos   = photos;
    v.complete = complete;
    v.slides   = photos.map(buildSlide);
    v.track.textContent = '';
    v.slides.forEach(function (slide) { v.track.appendChild(slide) });

    v.index = -1;
    v.track.scrollLeft = index * v.track.clientWidth;
    show(index);

    v.observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (entry.intersectionRatio >= 0.5) show(Number(entry.target.dataset.index));
      });
    }, { root: v.track, threshold: 0.5 });
    v.slides.forEach(function (slide) { v.observer.observe(slide) });
  }

  function open (listUrl, photo, position) {
    if (viewer) return;

    var root = el('div', 'viewer');
    var track = el('div', 'viewer-track');
    var cap = el('div', 'viewer-caption');
    var close = el('button', 'viewer-close');
    close.type = 'button';
    close.setAttribute('aria-label', 'Close');
    close.textContent = '×';
    root.appendChild(track);
    root.appendChild(cap);
    root.appendChild(close);

    document.body.appendChild(root);
    document.documentElement.classList.add('viewing');

    viewer = { root: root, track: track, caption: cap, photos: [], slides: [], index: -1, complete: false };
    history.pushState(STATE, '', photo.url);
    fill([ photo ], 0, false);

    enterFullscreen(root);
    keepAwake();

    fetch(listUrl).then(function (res) { return res.json() }).then(function (data) {
      if (! viewer || viewer.root !== root || data.format !== FORMAT) return;

      // The page's position is trusted when it agrees with the list, since
      // a photo can be in an album twice; otherwise the photo is looked up.
      var i = position - 1;
      if (! (data.photos[i] && data.photos[i].id === photo.id)) {
        i = data.photos.findIndex(function (p) { return p.id === photo.id });
        if (i < 0) return;
      }

      fill(data.photos, i, true);
    }).catch(noop);
  }

  function close () {
    var v = viewer;
    if (! v) return;
    viewer = null;

    stopVideo(v, v.index);
    if (v.observer) v.observer.disconnect();
    v.root.remove();
    document.documentElement.classList.remove('viewing');
    exitFullscreen();
    if (v.lock) v.lock.release().catch(noop);

    // Closing on the album page lands on the photo that was showing.
    var photo = v.photos[v.index];
    var tile = photo && document.querySelector('.grid a[href="' + photo.url + '"]');
    if (tile) tile.scrollIntoView({ block: 'center' });
  }

  // Closing goes back through history when the viewer's entry is current,
  // so the back gesture and the close button leave the same history.
  function requestClose () {
    if (! viewer) return;
    if (history.state && history.state.jiggleViewer) history.back();
    else close();
  }

  function openFrom (button) {
    open(button.dataset.list, JSON.parse(button.dataset.photo), Number(button.dataset.position));
  }

  document.addEventListener('click', function (e) {
    if (! e.target.closest) return;

    var button = e.target.closest('.viewer-open');
    if (button) {
      e.preventDefault();
      openFrom(button);
      return;
    }

    if (! viewer) {
      // On a photo's page in an album, the photo itself opens the viewer.
      if (e.target.closest('.photo figure img')) {
        var opener = document.querySelector('.viewer-open');
        if (opener) openFrom(opener);
      }
      return;
    }

    if (e.target.closest('.viewer-close')) {
      requestClose();
    } else if (e.target.closest('.viewer-play')) {
      playVideo(e.target.closest('.viewer-slide'));
    } else if (e.target.closest('.viewer-track') && ! e.target.closest('video')) {
      viewer.root.classList.toggle('viewer-captions');
    }
  });

  // Registered for the capture phase so that jiggle-keys.js, which follows
  // the page's prev and next links on arrows, sees the event already handled.
  document.addEventListener('keydown', function (e) {
    if (! viewer || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    if (e.target.closest && e.target.closest('video')) return;

    if      (e.key === 'ArrowLeft')  step(-1);
    else if (e.key === 'ArrowRight') step(1);
    else if (e.key === 'Escape')     requestClose();
    else return;

    e.preventDefault();
  }, true);

  window.addEventListener('popstate', function () { close() });

  [ 'fullscreenchange', 'webkitfullscreenchange' ].forEach(function (name) {
    document.addEventListener(name, function () {
      if (viewer && ! isFullscreen()) requestClose();
    });
  });

  document.addEventListener('visibilitychange', function () {
    if (viewer && document.visibilityState === 'visible') keepAwake();
  });

  window.addEventListener('resize', function () {
    if (viewer) viewer.track.scrollLeft = viewer.index * viewer.track.clientWidth;
  });

  document.documentElement.classList.add('has-viewer');
})();
