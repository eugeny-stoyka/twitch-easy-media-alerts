// Chroma key shared by the app's window and the video page (both get this
// file inlined by Go). Pixels whose colour is close to the key colour in
// CbCr space become transparent, the same way OBS's Chroma Key filter works,
// with its Similarity / Smoothness / Spill reduction values (1..1000).

function hexToRgb(hex) {
  const n = parseInt(hex.slice(1), 16);
  return [(n >> 16 & 255) / 255, (n >> 8 & 255) / 255, (n & 255) / 255];
}

const KEY_VERTEX = `
attribute vec2 p;
varying vec2 uv;
void main() {
  uv = p * 0.5 + 0.5;
  gl_Position = vec4(p, 0.0, 1.0);
}`;

const KEY_FRAGMENT = `
precision mediump float;
varying vec2 uv;
uniform sampler2D tex;
uniform vec3 keyRGB;
uniform vec2 pixel;
uniform float similarity;
uniform float smoothness;
uniform float spill;

// BT.709 limited-range chroma, as in OBS.
vec2 cbcr(vec3 c) {
  return vec2(dot(c, vec3(-0.100644, -0.338572, 0.439216)) + 0.501961,
              dot(c, vec3(0.439216, -0.398942, -0.040274)) + 0.501961);
}

void main() {
  vec2 key = cbcr(keyRGB);
  vec4 rgba = texture2D(tex, uv);
  // Average the distance over a 3x3 neighbourhood so noisy edges don't flicker.
  float d = 0.0;
  for (int x = -1; x <= 1; x++) {
    for (int y = -1; y <= 1; y++) {
      d += distance(key, cbcr(texture2D(tex, uv + vec2(float(x), float(y)) * pixel).rgb));
    }
  }
  float base = d / 9.0 - similarity;
  float alpha = pow(clamp(base / smoothness, 0.0, 1.0), 1.5);
  float keep = pow(clamp(base / spill, 0.0, 1.0), 1.5);
  float grey = dot(rgba.rgb, vec3(0.2126, 0.7152, 0.0722));
  vec3 rgb = mix(vec3(grey), rgba.rgb, keep); // desaturate the key colour's halo
  alpha *= rgba.a;                            // keep the picture's own transparency
  gl_FragColor = vec4(rgb * alpha, alpha);    // premultiplied
}`;

// createKeyer returns { canvas, draw, release } or null without WebGL. draw
// renders a video's current frame, an image or a VideoFrame (from
// ImageDecoder) into canvas at width x height, keyed with
// k = { color: "#rrggbb", similarity, smoothness, spill }. Set preserve to
// copy the result out of the canvas later (drawImage after the frame).
// release frees the WebGL context at once (browsers allow only a few).
function createKeyer(preserve) {
  const canvas = document.createElement("canvas");
  const gl = canvas.getContext("webgl", { alpha: true, premultipliedAlpha: true, antialias: false, preserveDrawingBuffer: !!preserve });
  if (!gl) return null;
  const shader = (type, src) => {
    const s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    return s;
  };
  const prog = gl.createProgram();
  gl.attachShader(prog, shader(gl.VERTEX_SHADER, KEY_VERTEX));
  gl.attachShader(prog, shader(gl.FRAGMENT_SHADER, KEY_FRAGMENT));
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return null;
  gl.useProgram(prog);

  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const pos = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(pos);
  gl.vertexAttribPointer(pos, 2, gl.FLOAT, false, 0, 0);

  gl.bindTexture(gl.TEXTURE_2D, gl.createTexture());
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
  gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, true);

  const u = name => gl.getUniformLocation(prog, name);
  const uKey = u("keyRGB"), uPixel = u("pixel"), uSim = u("similarity"), uSmooth = u("smoothness"), uSpill = u("spill");
  gl.clearColor(0, 0, 0, 0);

  return {
    canvas,
    draw(source, width, height, k) {
      if (canvas.width !== width || canvas.height !== height) {
        canvas.width = width;
        canvas.height = height;
      }
      gl.viewport(0, 0, width, height);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
      gl.uniform3fv(uKey, hexToRgb(k.color));
      const [sw, sh] = sourceSize(source);
      gl.uniform2f(uPixel, 1 / (sw || width), 1 / (sh || height));
      gl.uniform1f(uSim, k.similarity / 1000);
      gl.uniform1f(uSmooth, Math.max(k.smoothness, 1) / 1000);
      gl.uniform1f(uSpill, Math.max(k.spill, 1) / 1000);
      gl.clear(gl.COLOR_BUFFER_BIT);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    },
    release() {
      const lose = gl.getExtension("WEBGL_lose_context");
      if (lose) lose.loseContext();
    },
  };
}

// sourceSize returns the pixel size of a video, image or VideoFrame.
function sourceSize(s) {
  return [s.videoWidth || s.naturalWidth || s.displayWidth || 0, s.videoHeight || s.naturalHeight || s.displayHeight || 0];
}
