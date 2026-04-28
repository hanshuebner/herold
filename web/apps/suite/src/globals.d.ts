declare const __HEROLD_VERSION__: string;

declare module 'qrcode-svg' {
  interface QRCodeOptions {
    content: string;
    padding?: number;
    width?: number;
    height?: number;
    color?: string;
    background?: string;
    ecl?: 'L' | 'M' | 'Q' | 'H';
    join?: boolean;
    predefined?: boolean;
    pretty?: boolean;
    swap?: boolean;
    xmlDeclaration?: boolean;
    container?: 'svg' | 'svg-viewbox' | 'g' | 'none';
  }

  class QRCode {
    constructor(options: QRCodeOptions | string);
    svg(): string;
    save(file: string, callback?: (error: Error | null, result: string) => void): void;
  }

  export = QRCode;
}
