CREATE TABLE IF NOT EXISTS diseases (
    id SERIAL PRIMARY KEY,
    code VARCHAR(50) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Chèn dữ liệu mẫu (Ví dụ mã bệnh: D001 đến D010)
INSERT INTO diseases (code, name, description) VALUES
('D001', 'Cúm A (Influenza A)', 'Bệnh nhiễm trùng đường hô hấp cấp tính do virus cúm A gây ra.'),
('D002', 'Sốt xuất huyết Dengue', 'Bệnh truyền nhiễm cấp tính do virus Dengue gây muỗi vằn truyền.'),
('D003', 'Viêm phổi cấp', 'Tình trạng viêm mô phổi do vi khuẩn, virus hoặc nấm.'),
('D004', 'Đái tháo đường Type 2', 'Bệnh mạn tính liên quan đến chuyển hóa insulin.'),
('D005', 'Tăng huyết áp nguyên phát', 'Bệnh lý tim mạch phổ biến với áp lực máu thành mạch tăng cao.');