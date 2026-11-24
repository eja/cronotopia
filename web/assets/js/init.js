(function () {
  function applyPolyfills(scope) {
    if (typeof scope.AbortSignal !== 'undefined' && !scope.AbortSignal.prototype.throwIfAborted) {
      scope.AbortSignal.prototype.throwIfAborted = function () {
        if (this.aborted) {
          var err = new Error('This operation was aborted');
          err.name = 'AbortError';
          throw err;
        }
      };
    }

    function atPolyfill(n) {
      n = Math.trunc(n) || 0;
      if (n < 0) n += this.length;
      if (n < 0 || n >= this.length) return undefined;
      return this[n];
    }

    var targets = [
      scope.Array,
      scope.String,
      scope.Int8Array,
      scope.Uint8Array,
      scope.Uint8ClampedArray,
      scope.Int16Array,
      scope.Uint16Array,
      scope.Int32Array,
      scope.Uint32Array,
      scope.Float32Array,
      scope.Float64Array
    ];

    for (var i = 0; i < targets.length; i++) {
      var cls = targets[i];
      if (cls && !cls.prototype.at) {
        Object.defineProperty(cls.prototype, 'at', {
          value: atPolyfill,
          writable: true,
          enumerable: false,
          configurable: true
        });
      }
    }

    if (scope.Object && !scope.Object.hasOwn) {
      scope.Object.hasOwn = function (obj, prop) {
        return Object.prototype.hasOwnProperty.call(obj, prop);
      };
    }
  }

  applyPolyfills(window);

  if (typeof window.Worker !== 'undefined') {
    var NativeWorker = window.Worker;
    window.Worker = function (scriptUrl, options) {
      var workerBootstrap = '(' + applyPolyfills.toString() + ')(self);\nimportScripts(' + JSON.stringify(scriptUrl.toString()) + ');';
      var blob = new Blob([workerBootstrap], { type: 'application/javascript' });
      return new NativeWorker(URL.createObjectURL(blob), options);
    };
  }
})();
